package links

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu                      sync.Mutex
	messages                map[string]Message
	creates, edits, deletes int
	fetches                 int
	createErr, fetchErr     error
	editErr, deleteErr      error
	nonces                  []string
}

func messageKey(channel, id string) string { return channel + "/" + id }

func (a *fakeAPI) GetMessage(ctx context.Context, channelID, messageID string) (*Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fetches++
	if a.fetchErr != nil {
		return nil, a.fetchErr
	}
	msg, ok := a.messages[messageKey(channelID, messageID)]
	if !ok {
		return nil, nil
	}
	return &msg, ctx.Err()
}

func (a *fakeAPI) CreateReply(ctx context.Context, source Message, content, nonce string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.createErr != nil {
		return "", a.createErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.creates++
	a.nonces = append(a.nonces, nonce)
	id := fmt.Sprint(1000000 + a.creates)
	a.messages[messageKey(source.ChannelID, id)] = Message{ID: id, ChannelID: source.ChannelID, GuildID: source.GuildID, Content: content, Bot: true}
	return id, nil
}

func (a *fakeAPI) EditReply(ctx context.Context, channelID, messageID, content string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.editErr != nil {
		return false, a.editErr
	}
	msg, ok := a.messages[messageKey(channelID, messageID)]
	if !ok {
		return false, nil
	}
	a.edits++
	msg.Content = content
	a.messages[messageKey(channelID, messageID)] = msg
	return true, ctx.Err()
}

func (a *fakeAPI) DeleteReply(ctx context.Context, channelID, messageID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleteErr != nil {
		return a.deleteErr
	}
	a.deletes++
	delete(a.messages, messageKey(channelID, messageID))
	return ctx.Err()
}

func (a *fakeAPI) source(msg Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages[messageKey(msg.ChannelID, msg.ID)] = msg
}

func newHarness(t *testing.T) (*Controller, *fakeAPI, *Store, Message) {
	t.Helper()
	s := newTestStore(t)
	a := &fakeAPI{messages: make(map[string]Message)}
	c, err := NewController(Config{GuildID: "100", Now: func() time.Time { return testTime }}, s, a)
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "300", ChannelID: "200", GuildID: "100", Content: "post https://x.com/u/status/123"}
	a.source(msg)
	return c, a, s, msg
}

func TestControllerCreateDuplicateEditAndRemoveLink(t *testing.T) {
	c, a, s, msg := newHarness(t)
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Lookup(msg.ID)
	if err := c.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if a.creates != 1 || a.edits != 0 || a.nonces[0] != msg.ID {
		t.Fatalf("duplicate event resent: %#v", a)
	}
	msg.Content = "https://twitter.com/v/status/456?s=20 @everyone"
	a.source(msg)
	if err := c.Refresh(ctx, msg.ChannelID, msg.ID); err != nil {
		t.Fatal(err)
	}
	changed, _ := s.Lookup(msg.ID)
	if changed.ReplyID != first.ReplyID || a.edits != 1 || a.creates != 1 {
		t.Fatal("edit did not update existing reply")
	}
	if got := a.messages[messageKey(msg.ChannelID, first.ReplyID)].Content; got != "https://fxtwitter.com/v/status/456" {
		t.Fatalf("unexpected reply or mention: %q", got)
	}
	msg.Content = "link removed"
	a.source(msg)
	if err := c.Refresh(ctx, msg.ChannelID, msg.ID); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 || a.deletes != 1 {
		t.Fatal("removed link left a reply or state")
	}
	if _, exists := a.messages[messageKey(msg.ChannelID, msg.ID)]; !exists {
		t.Fatal("original message was deleted")
	}
}

func TestControllerIgnoresBotWebhookAndOutOfScope(t *testing.T) {
	c, a, _, msg := newHarness(t)
	for _, change := range []func(*Message){
		func(m *Message) { m.Bot = true },
		func(m *Message) { m.Webhook = true },
		func(m *Message) { m.GuildID = "999" },
		func(m *Message) { m.ID = "invalid" },
	} {
		copy := msg
		change(&copy)
		if err := c.Handle(context.Background(), copy); err != nil {
			t.Fatal(err)
		}
	}
	c.channels["777"] = true
	if err := c.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if a.creates != 0 {
		t.Fatal("created out-of-scope reply")
	}
}

func TestControllerUntrackedEditDoesNotBackfill(t *testing.T) {
	c, a, _, msg := newHarness(t)
	if err := c.Refresh(context.Background(), msg.ChannelID, msg.ID); err != nil {
		t.Fatal(err)
	}
	if a.creates != 0 || a.fetches != 0 {
		t.Fatal("untracked edit triggered network I/O")
	}
}

func TestControllerRecreatesMissingReplyWithNewNonce(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			c, a, s, msg := newHarness(t)
			ctx := context.Background()
			if err := c.Handle(ctx, msg); err != nil {
				t.Fatal(err)
			}
			first, _ := s.Lookup(msg.ID)
			delete(a.messages, messageKey(msg.ChannelID, first.ReplyID))
			if changed {
				msg.Content = "https://x.com/u/status/456"
				a.source(msg)
			}
			if err := c.Refresh(ctx, msg.ChannelID, msg.ID); err != nil {
				t.Fatal(err)
			}
			second, _ := s.Lookup(msg.ID)
			if a.creates != 2 || second.ReplyID == first.ReplyID || second.Generation != 1 || a.nonces[0] == a.nonces[1] {
				t.Fatal("missing reply was not safely recreated")
			}
		})
	}
}

func TestControllerReconcileCatchesOfflineDelete(t *testing.T) {
	c, a, s, msg := newHarness(t)
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	delete(a.messages, messageKey(msg.ChannelID, msg.ID))
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 || a.deletes != 1 {
		t.Fatal("offline deletion was not reconciled")
	}
}

func TestControllerAccessFailureDoesNotForgetMapping(t *testing.T) {
	c, a, s, msg := newHarness(t)
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	a.fetchErr = errors.New("403 forbidden")
	if err := c.Refresh(ctx, msg.ChannelID, msg.ID); err == nil {
		t.Fatal("expected fetch error")
	}
	if s.Count() != 1 || a.deletes != 0 {
		t.Fatal("access error treated as deletion")
	}
	a.deleteErr = errors.New("403 forbidden")
	if err := c.Delete(ctx, msg.ChannelID, msg.ID); err == nil || s.Count() != 1 {
		t.Fatal("failed delete forgot mapping")
	}
}

func TestControllerPendingSendSurvivesFailureAndRestart(t *testing.T) {
	c, a, s, msg := newHarness(t)
	ctx := context.Background()
	a.createErr = errors.New("temporary outage")
	if err := c.Handle(ctx, msg); err == nil {
		t.Fatal("expected send failure")
	}
	pending, ok := s.Lookup(msg.ID)
	if !ok || pending.ReplyID != "" {
		t.Fatal("pending send was not durable")
	}
	a.createErr = nil
	reopened, err := OpenStore(s.path, "100", testTime)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(c.cfg, reopened, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, _ := reopened.Lookup(msg.ID); rec.ReplyID == "" || a.creates != 1 {
		t.Fatal("pending send did not recover")
	}
}

func TestControllerDoesNotSendIfPendingPersistenceFails(t *testing.T) {
	c, a, s, msg := newHarness(t)
	s.write = func(string, []byte) error { return errors.New("disk full") }
	if err := c.Handle(context.Background(), msg); err == nil {
		t.Fatal("expected write error")
	}
	if a.creates != 0 || s.Count() != 0 {
		t.Fatal("untracked send occurred")
	}
}

func TestControllerPreparePersistsWithoutNetworkAndRecovers(t *testing.T) {
	c, a, s, msg := newHarness(t)
	if err := c.Prepare(msg); err != nil {
		t.Fatal(err)
	}
	if rec, ok := s.Lookup(msg.ID); !ok || rec.ReplyID != "" || a.creates != 0 || a.fetches != 0 {
		t.Fatal("Prepare must persist a pending source without network I/O")
	}
	if err := c.Prepare(msg); err != nil || s.Count() != 1 {
		t.Fatal("Prepare was not idempotent")
	}
	a.fetchErr = errors.New("temporary fetch outage")
	if err := c.Reconcile(context.Background()); err == nil || s.Count() != 1 {
		t.Fatal("fetch failure forgot prepared source")
	}
	a.fetchErr = nil
	if err := c.Reconcile(context.Background()); err != nil || a.creates != 1 {
		t.Fatalf("prepared source failed to recover: %v", err)
	}
}

func TestControllerPrepareFailureDoesNotUseNetwork(t *testing.T) {
	c, a, s, msg := newHarness(t)
	s.write = func(string, []byte) error { return errors.New("disk full") }
	if err := c.Prepare(msg); err == nil || a.creates != 0 || a.fetches != 0 || s.Count() != 0 {
		t.Fatal("failed Prepare performed side effects")
	}
}

func TestControllerRetainsUncommittedReplyAndRetriesCommit(t *testing.T) {
	c, a, s, msg := newHarness(t)
	write := s.write
	calls := 0
	s.write = func(path string, data []byte) error {
		calls++
		if calls == 2 {
			return errors.New("disk full")
		}
		return write(path, data)
	}
	if err := c.Handle(context.Background(), msg); err == nil {
		t.Fatal("expected commit error")
	}
	if a.creates != 1 || a.deletes != 0 {
		t.Fatal("uncommitted reply should remain available without retiring its nonce")
	}
	if rec, ok := s.Lookup(msg.ID); !ok || rec.ReplyID != "" {
		t.Fatal("pending state was lost")
	}
	if err := c.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if rec, ok := s.Lookup(msg.ID); !ok || rec.ReplyID == "" || a.creates != 1 || a.deletes != 0 {
		t.Fatal("commit retry resent or deleted the successful reply")
	}
	if len(c.uncommitted) != 0 {
		t.Fatal("committed journal entry was not released")
	}
}

func TestControllerPendingCommitBlocksResendUntilDiskRecovers(t *testing.T) {
	c, a, s, msg := newHarness(t)
	write := s.write
	calls := 0
	s.write = func(path string, data []byte) error {
		calls++
		if calls >= 2 {
			return errors.New("disk full")
		}
		return write(path, data)
	}
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err == nil {
		t.Fatal("expected initial commit failure")
	}
	for i := 0; i < 3; i++ {
		if err := c.Handle(ctx, msg); err == nil {
			t.Fatal("expected persistent commit failure")
		}
	}
	if a.creates != 1 || a.deletes != 0 {
		t.Fatal("disk error caused new Discord side effects")
	}
	s.write = write
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if a.creates != 1 || len(c.uncommitted) != 0 {
		t.Fatal("reconciliation did not commit existing reply")
	}
}

func TestControllerSourceDeleteAfterFailedReplyCommit(t *testing.T) {
	c, a, s, msg := newHarness(t)
	write := s.write
	calls := 0
	s.write = func(path string, data []byte) error {
		calls++
		if calls == 2 {
			return errors.New("disk full")
		}
		return write(path, data)
	}
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err == nil {
		t.Fatal("expected commit error")
	}
	delete(a.messages, messageKey(msg.ChannelID, msg.ID))
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if a.creates != 1 || a.deletes != 1 || s.Count() != 0 || len(c.uncommitted) != 0 {
		t.Fatal("source deletion left an uncommitted reply or record")
	}
}

func TestControllerRetentionLeavesExistingReply(t *testing.T) {
	c, a, s, msg := newHarness(t)
	ctx := context.Background()
	if err := c.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Lookup(msg.ID)
	c.cfg.Now = func() time.Time { return testTime.Add(31 * 24 * time.Hour) }
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 || a.deletes != 0 {
		t.Fatal("expiry deleted reply or retained mapping")
	}
	if _, ok := a.messages[messageKey(msg.ChannelID, rec.ReplyID)]; !ok {
		t.Fatal("existing reply was removed")
	}
}

func TestControllerConcurrentDuplicatesSendOnce(t *testing.T) {
	c, a, _, msg := newHarness(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Handle(context.Background(), msg)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if a.creates != 1 {
		t.Fatalf("created %d duplicate replies", a.creates)
	}
}

func TestControllerCapacityAndCancellation(t *testing.T) {
	c, a, s, msg := newHarness(t)
	c.cfg.MaxRecords = 1
	if err := c.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	msg.ID = "301"
	if err := c.Handle(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity error = %v", err)
	}
	if a.creates != 1 || s.Count() != 1 {
		t.Fatal("capacity limit exceeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Handle(ctx, msg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestControllerConcurrentCapacityIsBounded(t *testing.T) {
	c, a, s, msg := newHarness(t)
	c.cfg.MaxRecords = 1
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := msg
			copy.ID = fmt.Sprint(300 + i)
			err := c.Handle(context.Background(), copy)
			if err != nil && !strings.Contains(err.Error(), "capacity") {
				t.Errorf("unexpected concurrent error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if s.Count() != 1 || a.creates != 1 {
		t.Fatalf("concurrent capacity exceeded: records=%d replies=%d", s.Count(), a.creates)
	}
}

func TestSendNonceFitsDiscordLimit(t *testing.T) {
	rec := Record{MessageID: "18446744073709551615", Generation: ^uint32(0)}
	if nonce := sendNonce(rec); len(nonce) > 25 || !strings.Contains(nonce, "-") {
		t.Fatalf("invalid replacement nonce %q", nonce)
	}
}
