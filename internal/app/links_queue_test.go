package app

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/links"
)

type queueFake struct {
	mu                               sync.Mutex
	tracked                          bool
	prepared, sent, deleted, fetched int
	readStarted                      chan struct{}
	allowRead                        chan struct{}
	done                             chan struct{}
	msg                              links.Message
}

func (f *queueFake) Eligible(links.Message) bool { return true }
func (f *queueFake) Has(string) bool             { f.mu.Lock(); defer f.mu.Unlock(); return f.tracked }
func (f *queueFake) Prepare(links.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	f.tracked = true
	return nil
}
func (f *queueFake) Handle(context.Context, links.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent++
	return nil
}
func (f *queueFake) Refresh(context.Context, string, string) error { return nil }
func (f *queueFake) Delete(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted++
	f.tracked = false
	select {
	case f.done <- struct{}{}:
	default:
	}
	return nil
}
func (f *queueFake) GetMessage(ctx context.Context, _, _ string) (*links.Message, error) {
	f.mu.Lock()
	f.fetched++
	first := f.fetched == 1
	prepared := f.prepared
	f.mu.Unlock()
	if prepared == 0 {
		panic("source read occurred before durable Prepare")
	}
	if first {
		close(f.readStarted)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.allowRead:
		copy := f.msg
		return &copy, nil
	}
}

func TestDeleteWhileCreateRunningIsNotLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &queueFake{readStarted: make(chan struct{}), allowRead: make(chan struct{}), done: make(chan struct{}, 1), msg: links.Message{
		ID: "12345", ChannelID: "23456", GuildID: "34567", Content: "https://x.com/user/status/12345",
	}}
	q := newLinkQueue(ctx, 2, 16, f, f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer func() { cancel(); q.wait() }()
	q.create(f.msg)
	select {
	case <-f.readStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not fetch")
	}
	// Duplicate creation and edits are coalesced, then deletion takes priority.
	for i := 0; i < 20; i++ {
		q.create(f.msg)
		q.update(f.msg.ChannelID, f.msg.ID)
	}
	q.delete(f.msg.ChannelID, f.msg.ID)
	close(f.allowRead)
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("queued deletion was lost")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepared != 1 || f.sent != 1 || f.deleted != 1 || f.fetched != 1 || f.tracked {
		t.Fatalf("unexpected processing prepared=%d sent=%d deleted=%d fetched=%d tracked=%v", f.prepared, f.sent, f.deleted, f.fetched, f.tracked)
	}
}

func TestUnknownEditsDoNotBackfillAndQueueStaysBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &queueFake{readStarted: make(chan struct{}), allowRead: make(chan struct{}), done: make(chan struct{}, 1), msg: links.Message{
		ID: "12345", ChannelID: "23456", GuildID: "34567", Content: "https://x.com/user/status/12345",
	}}
	q := newLinkQueue(ctx, 1, 1, f, f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer func() { cancel(); q.wait() }()
	q.update(f.msg.ChannelID, "99999")
	q.mu.Lock()
	n := len(q.pending)
	q.mu.Unlock()
	if n != 0 {
		t.Fatal("untracked edit was queued")
	}
	q.create(f.msg)
	select {
	case <-f.readStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not fetch")
	}
	another := f.msg
	another.ID = "54321"
	q.create(another)
	q.mu.Lock()
	n = len(q.pending)
	q.mu.Unlock()
	if n != 1 {
		t.Fatalf("queue exceeded capacity: %d", n)
	}
}
