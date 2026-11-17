package owner

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Channel is a fresh REST snapshot, not an old gateway cache entry.
type Channel struct {
	ID         string
	GuildID    string
	ParentID   string
	Type       int
	CreatedAt  int64 // Unix milliseconds; derive from the channel snowflake.
	Flags      uint64
	Overwrites []Overwrite
}

type API interface {
	// FetchChannel returns nil only for Discord's Unknown Channel (404).
	FetchChannel(context.Context, string) (*Channel, error)
	WriteOverwrites(context.Context, string, []Overwrite, string) (*Channel, error)
	FindAudit(context.Context, string) (*AuditEntry, error)
}

type Config struct {
	GuildID       string
	BotID         string
	CategoryIDs   []string
	Workers       int
	QueueCapacity int
	RetryDelays   []time.Duration // nil uses 0, 750ms, 1500ms, 3000ms.
}

type Result string

const (
	ResultDeleted Result = "deleted"
	ResultIgnored Result = "ignored"
	ResultOwned   Result = "owned"
	ResultPending Result = "pending"
)

var (
	ErrQueueFull  = errors.New("owner reconciliation queue is full; periodic scan will retry")
	ErrNotStarted = errors.New("owner controller has not started")
	ErrObfuscated = errors.New("channel metadata is obfuscated: grant the bot View Channel before retrying")
)

type pendingWork struct{ lookUp, running, repeat bool }
type auditItem struct {
	entry   AuditEntry
	element *list.Element
}
type keyedGate struct {
	token chan struct{}
	refs  int
}

type Controller struct {
	config     Config
	categories map[string]struct{}
	store      *Store
	api        API
	log        *slog.Logger
	now        func() time.Time
	slots      chan struct{}

	gateMu sync.Mutex
	gates  map[string]*keyedGate

	auditMu       sync.Mutex
	evidence      map[string]auditItem
	evidenceOrder *list.List

	startMu sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	queue   chan string
	queueMu sync.Mutex
	pending map[string]*pendingWork
}

func NewController(config Config, store *Store, api API, logger *slog.Logger) (*Controller, error) {
	if !ValidID(config.GuildID) || !ValidID(config.BotID) || config.GuildID == config.BotID || len(config.CategoryIDs) == 0 || store == nil || api == nil || store.GuildID() != config.GuildID {
		return nil, fmt.Errorf("invalid owner controller configuration")
	}
	if config.Workers == 0 {
		config.Workers = 4
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = 1024
	}
	if config.Workers < 1 || config.Workers > 64 || config.QueueCapacity < 1 || config.QueueCapacity > 100000 {
		return nil, fmt.Errorf("invalid owner workers or queue capacity")
	}
	if config.RetryDelays == nil {
		config.RetryDelays = []time.Duration{0, 750 * time.Millisecond, 1500 * time.Millisecond, 3000 * time.Millisecond}
	}
	config.RetryDelays = append([]time.Duration(nil), config.RetryDelays...)
	for _, d := range config.RetryDelays {
		if d < 0 || d > time.Minute {
			return nil, fmt.Errorf("invalid owner retry delay")
		}
	}
	categories := make(map[string]struct{}, len(config.CategoryIDs))
	for _, id := range config.CategoryIDs {
		if !ValidID(id) {
			return nil, fmt.Errorf("invalid category ID: %q", id)
		}
		categories[id] = struct{}{}
	}
	config.CategoryIDs = append([]string(nil), config.CategoryIDs...)
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Controller{config: config, categories: categories, store: store, api: api, log: logger, now: time.Now, slots: make(chan struct{}, config.Workers), gates: map[string]*keyedGate{}, evidence: map[string]auditItem{}, evidenceOrder: list.New(), queue: make(chan string, config.QueueCapacity), pending: map[string]*pendingWork{}}, nil
}

func (c *Controller) InScope(channel *Channel) bool {
	if channel == nil || channel.GuildID != c.config.GuildID {
		return false
	}
	if _, ok := c.categories[channel.ParentID]; !ok {
		return false
	}
	switch channel.Type {
	case 0, 2, 5, 13, 15, 16:
		return true
	default:
		return false
	}
}

func (c *Controller) Eligible(channel *Channel) bool {
	if !c.InScope(channel) {
		return false
	}
	_, known := c.store.Get(channel.ID)
	return known || channel.CreatedAt >= c.store.ActivatedAt()
}

// RememberAudit keeps at most 1000 exact, valid creation entries. Gateway audit
// delivery may precede CHANNEL_CREATE; the periodic scan is still authoritative.
func (c *Controller) RememberAudit(entry AuditEntry) bool {
	if FindCreateEntry([]AuditEntry{entry}, entry.TargetID) == nil || entry.ExecutorID == c.config.GuildID {
		return false
	}
	c.auditMu.Lock()
	defer c.auditMu.Unlock()
	if previous, ok := c.evidence[entry.TargetID]; ok {
		previous.entry = entry
		c.evidence[entry.TargetID] = previous
		c.evidenceOrder.MoveToBack(previous.element)
		return true
	}
	element := c.evidenceOrder.PushBack(entry.TargetID)
	c.evidence[entry.TargetID] = auditItem{entry: entry, element: element}
	if len(c.evidence) > 1000 {
		first := c.evidenceOrder.Front()
		delete(c.evidence, first.Value.(string))
		c.evidenceOrder.Remove(first)
	}
	return true
}

func (c *Controller) cachedAudit(id string) *AuditEntry {
	c.auditMu.Lock()
	defer c.auditMu.Unlock()
	item, ok := c.evidence[id]
	if !ok {
		return nil
	}
	entry := item.entry
	return &entry
}
func (c *Controller) forgetAudit(id string) {
	c.auditMu.Lock()
	defer c.auditMu.Unlock()
	if item, ok := c.evidence[id]; ok {
		c.evidenceOrder.Remove(item.element)
		delete(c.evidence, id)
	}
}

// Start begins a bounded worker pool. Repeated calls are harmless. A Controller
// has one lifetime: construct a new controller after Close.
func (c *Controller) Start(ctx context.Context) {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.ctx != nil {
		return
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(c.config.Workers)
	for i := 0; i < c.config.Workers; i++ {
		go c.worker(c.ctx)
	}
}

// Request coalesces queued/running requests for the same channel without
// spawning a goroutine. A full queue returns ErrQueueFull for the caller to log;
// the periodic fresh scan must retry all eligible/known channel IDs.
func (c *Controller) Request(id string, lookUp bool) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid channel ID: %q", id)
	}
	c.startMu.Lock()
	ctx := c.ctx
	c.startMu.Unlock()
	if ctx == nil {
		return ErrNotStarted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	if work, ok := c.pending[id]; ok {
		work.lookUp = work.lookUp || lookUp
		if work.running {
			work.repeat = true
		}
		return nil
	}
	c.pending[id] = &pendingWork{lookUp: lookUp}
	select {
	case c.queue <- id:
		return nil
	default:
		delete(c.pending, id)
		return ErrQueueFull
	}
}

func (c *Controller) worker(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-c.queue:
			for {
				if ctx.Err() != nil {
					return
				}
				c.queueMu.Lock()
				work := c.pending[id]
				if work == nil {
					c.queueMu.Unlock()
					break
				}
				lookUp := work.lookUp
				work.lookUp, work.repeat, work.running = false, false, true
				c.queueMu.Unlock()
				if _, err := c.Reconcile(ctx, id, lookUp); err != nil && ctx.Err() == nil {
					c.log.Error("owner reconciliation failed", "channel_id", id, "error", err)
				}
				c.queueMu.Lock()
				if !work.repeat {
					delete(c.pending, id)
					c.queueMu.Unlock()
					break
				}
				work.running = false
				// Yield to other IDs when queue space is available. If full,
				// retain this worker so a coalesced update is never dropped.
				select {
				case c.queue <- id:
					c.queueMu.Unlock()
					goto next
				default:
					c.queueMu.Unlock()
				}
			}
		}
	next:
	}
}

// Close cancels the worker pool and waits for context-aware API calls to return.
func (c *Controller) Close() {
	c.startMu.Lock()
	cancel := c.cancel
	c.startMu.Unlock()
	if cancel != nil {
		cancel()
		c.wg.Wait()
	}
}

func (c *Controller) acquire(ctx context.Context, id string) (func(), error) {
	c.gateMu.Lock()
	gate, ok := c.gates[id]
	if !ok {
		gate = &keyedGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		c.gates[id] = gate
	}
	gate.refs++
	c.gateMu.Unlock()
	releaseRef := func() {
		c.gateMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(c.gates, id)
		}
		c.gateMu.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-gate.token:
	}
	select {
	case <-ctx.Done():
		gate.token <- struct{}{}
		releaseRef()
		return nil, ctx.Err()
	case c.slots <- struct{}{}:
	}
	return func() { <-c.slots; gate.token <- struct{}{}; releaseRef() }, nil
}

// Reconcile is safe for concurrent callers: one channel is processed at a time,
// and at most Config.Workers channels may perform network work concurrently.
func (c *Controller) Reconcile(ctx context.Context, id string, lookUp bool) (Result, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid channel ID: %q", id)
	}
	release, err := c.acquire(ctx, id)
	if err != nil {
		return "", err
	}
	defer release()
	return c.reconcile(ctx, id, lookUp)
}

func (c *Controller) fetch(ctx context.Context, id string) (*Channel, error) {
	channel, err := c.api.FetchChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if channel != nil && channel.ID != id {
		return nil, fmt.Errorf("Discord returned a different channel ID")
	}
	if channel != nil && channel.Flags&ChannelObfuscated != 0 {
		return nil, ErrObfuscated
	}
	return channel, nil
}

func (c *Controller) apply(ctx context.Context, channel *Channel, ownerID string) (bool, error) {
	if channel.Flags&ChannelObfuscated != 0 {
		return false, ErrObfuscated
	}
	desired, err := BuildOverwrites(channel.Overwrites, c.config.GuildID, c.config.BotID, ownerID, channel.Type == 2 || channel.Type == 13)
	if err != nil {
		return false, err
	}
	equal, err := EqualOverwrites(channel.Overwrites, desired)
	if err != nil || equal {
		return false, err
	}
	reason := "Restrict management while awaiting creator audit evidence"
	if ownerID != "" {
		reason = "Channel owner: " + ownerID
	}
	updated, err := c.api.WriteOverwrites(ctx, channel.ID, desired, reason)
	if err != nil {
		return false, err
	}
	if updated == nil || updated.ID != channel.ID || updated.Flags&ChannelObfuscated != 0 {
		return false, fmt.Errorf("Discord did not return the requested channel permissions")
	}
	equal, err = EqualOverwrites(updated.Overwrites, desired)
	if err != nil {
		return false, err
	}
	if !equal {
		return false, fmt.Errorf("Discord did not return the requested permission overwrites")
	}
	return true, nil
}

func (c *Controller) assignEvidence(id string, record Record, entry *AuditEntry) (Record, error) {
	if entry == nil {
		return record, nil
	}
	match := FindCreateEntry([]AuditEntry{*entry}, id)
	if match == nil || match.ExecutorID == c.config.GuildID {
		return record, nil
	}
	owned := record
	owned.OwnerID, owned.AuditID = match.ExecutorID, match.ID
	// Persist evidence before granting any member management permissions.
	if err := c.store.Put(id, owned); err != nil {
		return record, err
	}
	c.forgetAudit(id)
	return owned, nil
}

func (c *Controller) reconcile(ctx context.Context, id string, lookUp bool) (Result, error) {
	channel, err := c.fetch(ctx, id)
	if err != nil {
		return "", err
	}
	if channel == nil {
		c.forgetAudit(id)
		return ResultDeleted, c.store.Remove(id)
	}
	if !c.Eligible(channel) {
		return ResultIgnored, nil
	}
	record, known := c.store.Get(id)
	if !known {
		record = Record{CategoryID: channel.ParentID, CreatedAt: channel.CreatedAt}
		if err := c.store.Put(id, record); err != nil {
			return "", err
		}
	}
	if record.OwnerID == "" {
		record, err = c.assignEvidence(id, record, c.cachedAudit(id))
		if err != nil {
			return "", err
		}
	}
	changed, err := c.apply(ctx, channel, record.OwnerID)
	if err != nil {
		return "", err
	}
	if changed {
		record.LastAppliedAt = c.now().UnixMilli()
		if err := c.store.Put(id, record); err != nil {
			return "", err
		}
		c.log.Info("channel management updated", "channel_id", id, "owner_id", record.OwnerID)
	}
	if record.OwnerID != "" {
		return ResultOwned, nil
	}
	if !lookUp {
		return ResultPending, nil
	}
	for _, delay := range c.config.RetryDelays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		evidence := c.cachedAudit(id)
		if evidence == nil {
			evidence, err = c.api.FindAudit(ctx, id)
			if err != nil {
				return "", err
			}
		}
		if evidence == nil || FindCreateEntry([]AuditEntry{*evidence}, id) == nil || evidence.ExecutorID == c.config.GuildID {
			continue
		}
		record, err = c.assignEvidence(id, record, evidence)
		if err != nil {
			return "", err
		}
		// Refetch after the audit wait to preserve unrelated permission edits
		// and to respect channel moves or deletion while we were waiting.
		channel, err = c.fetch(ctx, id)
		if err != nil {
			return "", err
		}
		if channel == nil {
			c.forgetAudit(id)
			return ResultDeleted, c.store.Remove(id)
		}
		if !c.Eligible(channel) {
			return ResultIgnored, nil
		}
		changed, err = c.apply(ctx, channel, record.OwnerID)
		if err != nil {
			return "", err
		}
		if changed {
			record.LastAppliedAt = c.now().UnixMilli()
			if err := c.store.Put(id, record); err != nil {
				return "", err
			}
		}
		c.log.Info("channel owner assigned", "channel_id", id, "owner_id", record.OwnerID)
		return ResultOwned, nil
	}
	if !record.UnresolvedLogged {
		record.UnresolvedLogged = true
		if err := c.store.Put(id, record); err != nil {
			return "", err
		}
		c.log.Warn("channel creator unresolved; management remains restricted until exact audit evidence arrives", "channel_id", id)
	}
	return ResultPending, nil
}
