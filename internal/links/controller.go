package links

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Message struct {
	ID, ChannelID, GuildID, Content string
	Bot, Webhook                    bool
}

// API isolates Discord I/O. Only a Discord 404 means a missing message; access
// denial and transport errors must remain errors, so state is not discarded.
// Replies and edits must disable all mentions, including the replied-to user.
type API interface {
	GetMessage(context.Context, string, string) (*Message, error)
	CreateReply(context.Context, Message, string, string) (string, error)
	EditReply(context.Context, string, string, string) (bool, error)
	DeleteReply(context.Context, string, string) error
}

type Config struct {
	GuildID    string
	ChannelIDs []string
	MaxLinks   int
	Retention  time.Duration
	MaxRecords int
	Now        func() time.Time
}

// Controller serializes work for the same source message. The fixed lock table
// bounds lock memory; callers should additionally use a bounded event queue.
type Controller struct {
	cfg      Config
	channels map[string]bool
	store    *Store
	api      API
	locks    [256]sync.Mutex
	newMu    sync.Mutex
	// A successful REST send must not be repeated merely because the state
	// commit failed. Keep its result until the pending record can be committed.
	// Entries are bounded by persisted records and accessed under their source
	// lock as well as this short map lock.
	uncommittedMu sync.Mutex
	uncommitted   map[string]Record
}

func NewController(cfg Config, store *Store, api API) (*Controller, error) {
	if !validID(cfg.GuildID) || store == nil || api == nil || store.GuildID() != cfg.GuildID {
		return nil, errors.New("invalid link controller guild, state or API")
	}
	if cfg.MaxLinks == 0 {
		cfg.MaxLinks = 5
	}
	if cfg.MaxLinks < 1 || cfg.MaxLinks > 5 {
		return nil, errors.New("link MaxLinks must be between 1 and 5")
	}
	if cfg.Retention == 0 {
		cfg.Retention = 30 * 24 * time.Hour
	}
	if cfg.Retention < time.Hour || cfg.Retention > 365*24*time.Hour {
		return nil, errors.New("link retention must be between one hour and 365 days")
	}
	if cfg.MaxRecords == 0 {
		cfg.MaxRecords = 10000
	}
	if cfg.MaxRecords < 1 {
		return nil, errors.New("link MaxRecords must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Controller{cfg: cfg, store: store, api: api, channels: make(map[string]bool), uncommitted: make(map[string]Record)}
	for _, id := range cfg.ChannelIDs {
		if !validID(id) {
			return nil, fmt.Errorf("invalid link channel ID %q", id)
		}
		c.channels[id] = true
	}
	return c, nil
}

func (c *Controller) Has(messageID string) bool {
	_, ok := c.store.Lookup(messageID)
	return ok
}

func (c *Controller) Records() []Record { return c.store.Records() }

func (c *Controller) lock(id string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	mu := &c.locks[h.Sum32()%uint32(len(c.locks))]
	mu.Lock()
	return mu.Unlock
}

func (c *Controller) eligible(msg Message) bool {
	return validID(msg.ID) && validID(msg.ChannelID) && msg.GuildID == c.cfg.GuildID &&
		!msg.Bot && !msg.Webhook && (len(c.channels) == 0 || c.channels[msg.ChannelID])
}

// Eligible reports whether the guild/channel/author policy permits a message.
// Callers can combine it with Extract to avoid queuing irrelevant gateway work.
func (c *Controller) Eligible(msg Message) bool { return c.eligible(msg) }

// Prepare durably records a fixable MessageCreate before a caller fetches the
// latest source from Discord. If that fetch fails, reconciliation can retry the
// pending source after a reconnect without saving its original text.
func (c *Controller) Prepare(msg Message) error {
	if !c.eligible(msg) || len(Extract(msg.Content, c.cfg.MaxLinks)) == 0 {
		return nil
	}
	unlock := c.lock(msg.ID)
	defer unlock()
	if err := c.flushUncommitted(msg.ID); err != nil {
		return err
	}
	if rec, ok := c.store.Lookup(msg.ID); ok {
		if rec.ChannelID != msg.ChannelID {
			return errors.New("source message channel differs from saved state")
		}
		return nil
	}
	_, err := c.prepareLocked(msg)
	return err
}

func (c *Controller) prepareLocked(msg Message) (Record, error) {
	// Serialize capacity checks and initial writes without holding a lock over
	// any REST operation. Per-source locking is already held by the caller.
	c.newMu.Lock()
	defer c.newMu.Unlock()
	if c.store.Count() >= c.cfg.MaxRecords {
		return Record{}, errors.New("link state capacity reached; increase MaxRecords or shorten retention")
	}
	now := c.cfg.Now().UnixMilli()
	rec := Record{MessageID: msg.ID, ChannelID: msg.ChannelID, CreatedAt: now, UpdatedAt: now}
	if err := c.store.Put(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Caller holds the source lock. Never delete a successfully sent reply to
// compensate a disk error: its nonce can still resolve to the deleted ID.
func (c *Controller) rememberUncommitted(rec Record) {
	c.uncommittedMu.Lock()
	c.uncommitted[rec.MessageID] = rec
	c.uncommittedMu.Unlock()
}

func (c *Controller) flushUncommitted(messageID string) error {
	c.uncommittedMu.Lock()
	rec, ok := c.uncommitted[messageID]
	c.uncommittedMu.Unlock()
	if !ok {
		return nil
	}
	if err := c.store.Put(rec); err != nil {
		return fmt.Errorf("commit already sent link reply %s: %w", rec.ReplyID, err)
	}
	c.uncommittedMu.Lock()
	delete(c.uncommitted, messageID)
	c.uncommittedMu.Unlock()
	return nil
}

// Handle accepts a fresh MessageCreate only. Use Refresh for update events;
// Refresh never backfills an untracked historical message.
func (c *Controller) Handle(ctx context.Context, msg Message) error {
	if !c.eligible(msg) {
		return nil
	}
	unlock := c.lock(msg.ID)
	defer unlock()
	return c.handleLocked(ctx, msg, false)
}

// Refresh reconciles an already tracked source after an edit or reconnect.
// A fresh REST fetch also makes partial/out-of-order gateway edits harmless.
func (c *Controller) Refresh(ctx context.Context, channelID, messageID string) error {
	unlock := c.lock(messageID)
	defer unlock()
	if err := c.flushUncommitted(messageID); err != nil {
		return err
	}
	rec, ok := c.store.Lookup(messageID)
	if !ok || rec.ChannelID != channelID {
		return nil
	}
	if c.expired(rec) {
		return c.store.Remove(messageID)
	}
	msg, err := c.api.GetMessage(ctx, channelID, messageID)
	if err != nil {
		return fmt.Errorf("fetch source message %s: %w", messageID, err)
	}
	if msg == nil {
		return c.deleteLocked(ctx, rec)
	}
	if msg.ID != messageID || msg.ChannelID != channelID {
		return errors.New("source message response ID mismatch")
	}
	if !c.eligible(*msg) {
		return nil
	}
	return c.handleLocked(ctx, *msg, true)
}

func (c *Controller) handleLocked(ctx context.Context, msg Message, verifyReply bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.flushUncommitted(msg.ID); err != nil {
		return err
	}
	rec, exists := c.store.Lookup(msg.ID)
	if exists && rec.ChannelID != msg.ChannelID {
		return errors.New("source message channel differs from saved state")
	}
	if exists && c.expired(rec) {
		return c.store.Remove(msg.ID)
	}
	content := strings.Join(Extract(msg.Content, c.cfg.MaxLinks), "\n")
	if content == "" {
		if exists {
			return c.deleteLocked(ctx, rec)
		}
		return nil
	}
	hash := contentHash(content)
	if exists && rec.ReplyID != "" {
		if rec.ContentHash == hash {
			if !verifyReply {
				return nil
			}
			reply, err := c.api.GetMessage(ctx, rec.ChannelID, rec.ReplyID)
			if err != nil {
				return fmt.Errorf("verify link reply %s: %w", rec.ReplyID, err)
			}
			if reply != nil {
				if reply.ID != rec.ReplyID || reply.ChannelID != rec.ChannelID {
					return errors.New("reply response ID mismatch")
				}
				return nil
			}
		} else {
			found, err := c.api.EditReply(ctx, rec.ChannelID, rec.ReplyID, content)
			if err != nil {
				return fmt.Errorf("edit link reply %s: %w", rec.ReplyID, err)
			}
			if found {
				rec.ContentHash = hash
				rec.UpdatedAt = c.timestamp(rec)
				return c.store.Put(rec)
			}
		}
		// A confirmed missing reply is persisted before replacing it.
		if rec.Generation == math.MaxUint32 {
			return errors.New("link reply generation limit reached")
		}
		rec.ReplyID = ""
		rec.ContentHash = ""
		rec.Generation++
		rec.UpdatedAt = c.timestamp(rec)
		if err := c.store.Put(rec); err != nil {
			return err
		}
	}
	if !exists {
		var err error
		rec, err = c.prepareLocked(msg)
		if err != nil {
			return err
		}
	}
	// Nonces fit Discord's 25-character limit. A replacement has a distinct
	// nonce so deduplication cannot resurrect the ID of a deleted reply.
	// The adapter must send enforce_nonce=true.
	replyID, err := c.api.CreateReply(ctx, msg, content, sendNonce(rec))
	if err != nil {
		return fmt.Errorf("create link reply for %s: %w", msg.ID, err)
	}
	rec.ReplyID = replyID
	rec.ContentHash = hash
	rec.UpdatedAt = c.timestamp(rec)
	if err := validateRecord(rec); err != nil {
		return fmt.Errorf("invalid link reply response: %w", err)
	}
	if err := c.store.Put(rec); err != nil {
		c.rememberUncommitted(rec)
		return fmt.Errorf("commit sent link reply %s: %w", replyID, err)
	}
	return nil
}

func sendNonce(rec Record) string {
	if rec.Generation == 0 {
		return rec.MessageID
	}
	id, _ := strconv.ParseUint(rec.MessageID, 10, 64)
	return strconv.FormatUint(id, 36) + "-" + strconv.FormatUint(uint64(rec.Generation), 36)
}

func (c *Controller) timestamp(rec Record) int64 {
	now := c.cfg.Now().UnixMilli()
	if now < rec.UpdatedAt {
		return rec.UpdatedAt
	}
	return now
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (c *Controller) expired(rec Record) bool {
	return c.cfg.Now().Sub(time.UnixMilli(rec.CreatedAt)) >= c.cfg.Retention
}

// Delete removes only this bot's reply, never the user's original message.
func (c *Controller) Delete(ctx context.Context, channelID, messageID string) error {
	unlock := c.lock(messageID)
	defer unlock()
	if err := c.flushUncommitted(messageID); err != nil {
		return err
	}
	rec, ok := c.store.Lookup(messageID)
	if !ok || rec.ChannelID != channelID {
		return nil
	}
	if c.expired(rec) {
		return c.store.Remove(messageID)
	}
	return c.deleteLocked(ctx, rec)
}

func (c *Controller) deleteLocked(ctx context.Context, rec Record) error {
	if rec.ReplyID != "" {
		if err := c.api.DeleteReply(ctx, rec.ChannelID, rec.ReplyID); err != nil {
			return fmt.Errorf("delete link reply %s: %w", rec.ReplyID, err)
		}
	}
	return c.store.Remove(rec.MessageID)
}

// Reconcile retries pending work and catches edits/deletions while offline.
// It intentionally visits only saved records and never scans message history.
// Expired records stop being tracked; their existing replies remain in Discord.
func (c *Controller) Reconcile(ctx context.Context) error {
	var result error
	for _, rec := range c.store.Records() {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := c.Refresh(ctx, rec.ChannelID, rec.MessageID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
