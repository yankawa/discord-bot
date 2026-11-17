package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/astrapath-dev/discord-bot/internal/links"
)

var errLinkQueueFull = errors.New("link event queue is full; this new event could not be scheduled")

type linkProcessor interface {
	Eligible(links.Message) bool
	Has(string) bool
	Prepare(links.Message) error
	Handle(context.Context, links.Message) error
	Refresh(context.Context, string, string) error
	Delete(context.Context, string, string) error
}

type sourceReader interface {
	GetMessage(context.Context, string, string) (*links.Message, error)
}

type linkWork struct {
	channelID, messageID     string
	create, refresh, deleted bool
	initial                  *links.Message
}

// linkQueue bounds both memory and concurrent I/O. Pending events for the same
// message merge, including events arriving while that message is being handled.
type linkQueue struct {
	ctx        context.Context
	controller linkProcessor
	reader     sourceReader
	log        *slog.Logger
	capacity   int
	ready      chan string
	slots      chan struct{}
	mu         sync.Mutex
	pending    map[string]*linkWork
	wg         sync.WaitGroup
}

func newLinkQueue(ctx context.Context, workers, capacity int, controller linkProcessor, reader sourceReader, logger *slog.Logger, slots chan struct{}) *linkQueue {
	if slots == nil {
		slots = make(chan struct{}, workers)
	}
	q := &linkQueue{ctx: ctx, controller: controller, reader: reader, log: logger, capacity: capacity, ready: make(chan string, capacity), slots: slots, pending: make(map[string]*linkWork)}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

func (q *linkQueue) wait() { q.wg.Wait() }

func (q *linkQueue) create(msg links.Message) {
	if !q.controller.Eligible(msg) || len(links.Extract(msg.Content, 1)) == 0 {
		return
	}
	q.add(linkWork{channelID: msg.ChannelID, messageID: msg.ID, create: true, initial: &msg}, false)
}

func (q *linkQueue) update(channelID, messageID string) {
	q.add(linkWork{channelID: channelID, messageID: messageID, refresh: true}, true)
}

func (q *linkQueue) delete(channelID, messageID string) {
	q.add(linkWork{channelID: channelID, messageID: messageID, deleted: true}, true)
}

func (q *linkQueue) add(work linkWork, trackedOnly bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil {
		return
	}
	if existing, ok := q.pending[work.messageID]; ok {
		if existing.channelID != work.channelID {
			return
		}
		existing.create = existing.create || work.create
		existing.refresh = existing.refresh || work.refresh
		existing.deleted = existing.deleted || work.deleted
		if work.initial != nil {
			existing.initial = work.initial
		}
		return
	}
	if trackedOnly && !q.controller.Has(work.messageID) {
		return
	}
	if len(q.pending) >= q.capacity {
		q.log.Error("link_queue_full", "message_id", work.messageID, "error", errLinkQueueFull)
		return
	}
	copy := work
	q.pending[work.messageID] = &copy
	// Map capacity includes running work, so a newly added key always fits.
	q.ready <- work.messageID
}

func (q *linkQueue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case id := <-q.ready:
			for q.ctx.Err() == nil {
				q.mu.Lock()
				pending := q.pending[id]
				work := *pending
				pending.create, pending.refresh, pending.deleted = false, false, false
				pending.initial = nil
				q.mu.Unlock()
				var err error
				withSlot(q.ctx, q.slots, func() { err = q.process(work) })
				if err != nil && q.ctx.Err() == nil {
					q.log.Error("link_event_failed", "message_id", id, "error", err)
				}
				q.mu.Lock()
				pending = q.pending[id]
				more := pending.create || pending.refresh || pending.deleted
				if !more {
					delete(q.pending, id)
				}
				q.mu.Unlock()
				if !more {
					break
				}
			}
		}
	}
}

func (q *linkQueue) process(work linkWork) error {
	if work.deleted {
		return q.controller.Delete(q.ctx, work.channelID, work.messageID)
	}
	if work.create {
		if work.initial == nil {
			return errors.New("new link event has no source message")
		}
		// A transient failure of the first REST fetch must still be recoverable
		// by the next scan. This stores IDs only, never the message content.
		if err := q.controller.Prepare(*work.initial); err != nil {
			return err
		}
		// Fetch the current content: gateway callbacks may arrive out of order,
		// and the author may already have edited or deleted the original post.
		msg, err := q.reader.GetMessage(q.ctx, work.channelID, work.messageID)
		if err != nil {
			return err
		}
		if msg == nil {
			return q.controller.Delete(q.ctx, work.channelID, work.messageID)
		}
		if msg.ID != work.messageID || msg.ChannelID != work.channelID {
			return errors.New("source message response ID mismatch")
		}
		if err := q.controller.Handle(q.ctx, *msg); err != nil {
			return err
		}
	}
	if work.refresh {
		return q.controller.Refresh(q.ctx, work.channelID, work.messageID)
	}
	return nil
}
