package owner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu               sync.Mutex
	channel          *Channel
	audit            *AuditEntry
	writes           [][]Overwrite
	operations       []string
	onFetch          func(context.Context, string) error
	onAudit          func(context.Context)
	onWrite          func([]Overwrite) error
	badWriteResponse bool
}

func cloneChannel(channel *Channel) *Channel {
	if channel == nil {
		return nil
	}
	copy := *channel
	copy.Overwrites = append([]Overwrite(nil), channel.Overwrites...)
	return &copy
}
func (api *fakeAPI) FetchChannel(ctx context.Context, id string) (*Channel, error) {
	if api.onFetch != nil {
		if err := api.onFetch(ctx, id); err != nil {
			return nil, err
		}
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	api.operations = append(api.operations, "fetch")
	if api.channel != nil && api.channel.ID == id {
		return cloneChannel(api.channel), nil
	}
	return nil, nil
}
func (api *fakeAPI) WriteOverwrites(ctx context.Context, id string, overwrites []Overwrite, reason string) (*Channel, error) {
	if api.onWrite != nil {
		if err := api.onWrite(overwrites); err != nil {
			return nil, err
		}
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	api.operations = append(api.operations, "write")
	api.writes = append(api.writes, append([]Overwrite(nil), overwrites...))
	if api.badWriteResponse {
		return cloneChannel(api.channel), nil
	}
	api.channel.Overwrites = append([]Overwrite(nil), overwrites...)
	return cloneChannel(api.channel), nil
}
func (api *fakeAPI) FindAudit(ctx context.Context, id string) (*AuditEntry, error) {
	api.mu.Lock()
	api.operations = append(api.operations, "audit")
	api.mu.Unlock()
	if api.onAudit != nil {
		api.onAudit(ctx)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.audit == nil {
		return nil, nil
	}
	entry := *api.audit
	return &entry, nil
}

func newFixture(t *testing.T) (*Controller, *Store, *fakeAPI) {
	t.Helper()
	store := newTestStore(t)
	api := &fakeAPI{channel: &Channel{ID: "20", GuildID: "1", ParentID: "10", Type: 0, CreatedAt: 2000, Overwrites: []Overwrite{{ID: "1", Type: 0, Allow: ManageChannels}}}, audit: &AuditEntry{ID: "30", Action: 10, TargetID: "20", ExecutorID: "3"}}
	controller, err := NewController(Config{GuildID: "1", BotID: "2", CategoryIDs: []string{"10"}, RetryDelays: []time.Duration{0}}, store, api, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.now = func() time.Time { return time.UnixMilli(4000) }
	return controller, store, api
}

func TestControllerSealsBeforeAuditAndPreservesChangesDuringLookup(t *testing.T) {
	c, store, api := newFixture(t)
	api.onAudit = func(ctx context.Context) {
		api.mu.Lock()
		defer api.mu.Unlock()
		sealed := indexOverwrites(api.channel.Overwrites)
		if sealed["1"].Allow&management != 0 || sealed["1"].Deny&management != management {
			t.Error("audit lookup happened while management remained open")
		}
		if _, ok := sealed["3"]; ok {
			t.Error("owner permission granted before evidence")
		}
		api.channel.Overwrites = append(api.channel.Overwrites, Overwrite{ID: "4", Type: 0, Deny: 1 << 11, Allow: 1 << 60})
	}
	api.onWrite = func(overwrites []Overwrite) error {
		if entry, ok := indexOverwrites(overwrites)["3"]; ok && entry.Allow&ManageChannels != 0 {
			record, known := store.Get("20")
			if !known || record.OwnerID != "3" || record.AuditID != "30" {
				t.Error("ownership grant preceded durable evidence")
			}
		}
		return nil
	}
	result, err := c.Reconcile(context.Background(), "20", true)
	if err != nil || result != ResultOwned {
		t.Fatalf("result=%s err=%v", result, err)
	}
	if len(api.writes) != 2 {
		t.Fatalf("want seal and owner grant, got %d writes", len(api.writes))
	}
	got := indexOverwrites(api.channel.Overwrites)["4"]
	if got.Deny&(1<<11) == 0 || got.Allow&(1<<60) == 0 {
		t.Fatal("lost unrelated changes during audit wait")
	}
	record, ok := store.Get("20")
	if !ok || record.OwnerID != "3" || record.LastAppliedAt != 4000 {
		t.Fatalf("bad record: %+v", record)
	}
}

func TestControllerUsesCachedExactEvidenceAndDoesNotRepeatedlyWrite(t *testing.T) {
	c, _, api := newFixture(t)
	if !c.RememberAudit(*api.audit) {
		t.Fatal("valid evidence rejected")
	}
	result, err := c.Reconcile(context.Background(), "20", true)
	if err != nil || result != ResultOwned {
		t.Fatal(result, err)
	}
	for _, op := range api.operations {
		if op == "audit" {
			t.Fatal("unnecessary audit REST lookup")
		}
	}
	if len(api.writes) != 1 {
		t.Fatalf("expected direct assignment: %d", len(api.writes))
	}
	if _, err := c.Reconcile(context.Background(), "20", true); err != nil {
		t.Fatal(err)
	}
	if len(api.writes) != 1 {
		t.Fatal("idempotent reconcile rewrote ACL")
	}
	if len(c.evidence) != 0 || c.evidenceOrder.Len() != 0 {
		t.Fatal("consumed audit cache entry retained")
	}
}

func TestControllerUnrelatedAuditNeverGrantsOwnership(t *testing.T) {
	c, store, api := newFixture(t)
	api.audit.TargetID = "21"
	result, err := c.Reconcile(context.Background(), "20", true)
	if err != nil || result != ResultPending {
		t.Fatal(result, err)
	}
	record, _ := store.Get("20")
	if record.OwnerID != "" || !record.UnresolvedLogged {
		t.Fatalf("unexpected owner: %+v", record)
	}
	if len(api.writes) != 1 {
		t.Fatal("expected one seal only")
	}
}

func TestControllerIgnoresExistingChannelsButRepairsTrackedOnes(t *testing.T) {
	c, store, api := newFixture(t)
	api.channel.CreatedAt = 500
	result, err := c.Reconcile(context.Background(), "20", true)
	if err != nil || result != ResultIgnored || len(api.writes) != 0 {
		t.Fatal(result, err)
	}
	if _, ok := store.Get("20"); ok {
		t.Fatal("adopted pre-activation channel")
	}
	record := sampleRecord()
	record.CreatedAt = 500
	if err := store.Put("20", record); err != nil {
		t.Fatal(err)
	}
	result, err = c.Reconcile(context.Background(), "20", true)
	if err != nil || result != ResultOwned || len(api.writes) != 1 {
		t.Fatal(result, err)
	}
}

func TestControllerHonorsMoveAndDeletionDuringAuditWait(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			c, store, api := newFixture(t)
			api.onAudit = func(context.Context) {
				api.mu.Lock()
				defer api.mu.Unlock()
				if deleted {
					api.channel = nil
				} else {
					api.channel.ParentID = "99"
				}
			}
			result, err := c.Reconcile(context.Background(), "20", true)
			if err != nil {
				t.Fatal(err)
			}
			if len(api.writes) != 1 {
				t.Fatal("wrote owner grant after leaving scope")
			}
			if deleted {
				if result != ResultDeleted {
					t.Fatal(result)
				}
				if _, ok := store.Get("20"); ok {
					t.Fatal("deleted channel retained")
				}
			} else if result != ResultIgnored {
				t.Fatal(result)
			}
		})
	}
}

func TestControllerNeverRewritesObfuscatedMetadata(t *testing.T) {
	c, store, api := newFixture(t)
	api.channel.Flags = ChannelObfuscated
	_, err := c.Reconcile(context.Background(), "20", true)
	if !errors.Is(err, ErrObfuscated) {
		t.Fatalf("expected obfuscation guard: %v", err)
	}
	if len(api.writes) != 0 || len(store.IDs()) != 0 {
		t.Fatal("obfuscated channel mutated")
	}
}

func TestControllerRequiresEvidencePersistenceBeforeGrant(t *testing.T) {
	c, store, api := newFixture(t)
	failure := errors.New("disk full")
	api.onAudit = func(context.Context) { store.writeFile = func(string, []byte) error { return failure } }
	_, err := c.Reconcile(context.Background(), "20", true)
	if !errors.Is(err, failure) {
		t.Fatalf("lost persistence error: %v", err)
	}
	if len(api.writes) != 1 {
		t.Fatal("granted despite failed evidence persistence")
	}
	record, _ := store.Get("20")
	if record.OwnerID != "" {
		t.Fatal("failed evidence changed memory")
	}
}

func TestControllerVerifiesDiscordWriteResponse(t *testing.T) {
	c, _, api := newFixture(t)
	api.badWriteResponse = true
	_, err := c.Reconcile(context.Background(), "20", true)
	if err == nil {
		t.Fatal("accepted missing requested permissions")
	}
	for _, op := range api.operations {
		if op == "audit" {
			t.Fatal("looked up audit before successful seal")
		}
	}
}

func TestControllerRetryWaitIsCancelable(t *testing.T) {
	c, _, api := newFixture(t)
	c.config.RetryDelays = []time.Duration{time.Minute}
	written := make(chan struct{}, 1)
	api.onWrite = func([]Overwrite) error { written <- struct{}{}; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Reconcile(ctx, "20", true); done <- err }()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("no write")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry sleep ignored cancellation")
	}
}

func TestControllerBoundedQueueCoalescesRequests(t *testing.T) {
	base, store, api := newFixture(t)
	_ = base
	c, err := NewController(Config{GuildID: "1", BotID: "2", CategoryIDs: []string{"10"}, Workers: 1, QueueCapacity: 1, RetryDelays: []time.Duration{0}}, store, api, nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	api.onFetch = func(ctx context.Context, id string) error {
		if id == "20" {
			once.Do(func() {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
		return ctx.Err()
	}
	c.Start(context.Background())
	defer c.Close()
	if err := c.Request("20", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := c.Request("21", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := c.Request("20", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Request("22", false); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue not bounded: %v", err)
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for {
		record, ok := store.Get("20")
		if ok && record.OwnerID == "3" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("coalesced lookup request lost")
		case <-time.After(time.Millisecond):
		}
	}
	c.Close()
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	if len(c.gates) != 0 {
		t.Fatal("per-channel gate leaked")
	}
}

func TestControllerConcurrentReconcileDoesNotDuplicateWrites(t *testing.T) {
	c, _, api := newFixture(t)
	c.RememberAudit(*api.audit)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Reconcile(context.Background(), "20", true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(api.writes) != 1 {
		t.Fatalf("duplicate writes: %d", len(api.writes))
	}
}

func TestControllerAuditCacheIsBoundedAndRejectsInvalidEvidence(t *testing.T) {
	c, _, _ := newFixture(t)
	for i := 1; i <= 1100; i++ {
		c.RememberAudit(AuditEntry{ID: fmt.Sprint(i), Action: 10, TargetID: fmt.Sprint(i + 10000), ExecutorID: "3"})
	}
	if len(c.evidence) != 1000 || c.evidenceOrder.Len() != 1000 {
		t.Fatal("unbounded audit evidence")
	}
	if c.cachedAudit("10001") != nil {
		t.Fatal("old evidence not evicted")
	}
	if c.RememberAudit(AuditEntry{ID: "5", Action: 11, TargetID: "20", ExecutorID: "3"}) || c.RememberAudit(AuditEntry{ID: "5", Action: 10, TargetID: "20", ExecutorID: "1"}) {
		t.Fatal("invalid evidence accepted")
	}
}

func TestControllerScopeExcludesThreadsCategoriesAndOtherGuilds(t *testing.T) {
	c, _, api := newFixture(t)
	for _, kind := range []int{0, 2, 5, 13, 15, 16} {
		channel := cloneChannel(api.channel)
		channel.Type = kind
		if !c.InScope(channel) {
			t.Fatalf("type %d excluded", kind)
		}
	}
	for _, kind := range []int{1, 3, 4, 10, 11, 12} {
		channel := cloneChannel(api.channel)
		channel.Type = kind
		if c.InScope(channel) {
			t.Fatalf("type %d included", kind)
		}
	}
	channel := cloneChannel(api.channel)
	channel.GuildID = "9"
	if c.InScope(channel) {
		t.Fatal("other guild included")
	}
}
