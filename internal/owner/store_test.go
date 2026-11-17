package owner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "state.json"), "1", time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func sampleRecord() Record {
	return Record{CategoryID: "10", CreatedAt: 2000, OwnerID: "3", AuditID: "9", LastAppliedAt: 3000}
}

func TestStorePersistsActivationOwnershipAndNullablePendingState(t *testing.T) {
	s := newTestStore(t)
	want := sampleRecord()
	if err := s.Put("20", want); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("21", Record{CategoryID: "10", CreatedAt: 2000}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path, "1", time.UnixMilli(9000))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ActivatedAt() != 1000 {
		t.Fatal("activation reset")
	}
	got, ok := reopened.Get("20")
	if !ok || got != want {
		t.Fatalf("lost record: %+v", got)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"ownerId": null`)) || !bytes.Contains(raw, []byte(`"lastAppliedAt": null`)) {
		t.Fatal("nullable v1 state not preserved")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("state permissions too broad: %v", info.Mode())
	}
}

func TestStoreNeverResetsCorruptOrWrongGuildState(t *testing.T) {
	for _, raw := range []string{`{"version":`, `{"version":1,"guildId":"2","activatedAt":1000,"channels":{}}`, `{"version":1,"guildId":"1","activatedAt":1000,"channels":{"20":{"categoryId":"10","createdAt":2000,"ownerId":"3","auditId":null,"lastAppliedAt":null,"unresolvedLogged":false}}}`, `{"version":1,"guildId":"1","activatedAt":1000,"channels":{}} {}`, `{"version":1,"guildId":"1","activatedAt":1000,"channels":{"20":{"categoryId":"10","createdAt":2000}}}`, `{"version":1,"guildId":"1","activatedAt":1000,"channels":{"20":{"categoryId":"10","createdAt":2000,"ownerId":null,"auditId":null,"lastAppliedAt":null,"unresolvedLogged":null}}}`} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(path, "1", time.UnixMilli(2000)); err == nil {
			t.Fatalf("accepted corrupt state %s", raw)
		}
		still, _ := os.ReadFile(path)
		if string(still) != raw {
			t.Fatal("corrupt state was overwritten")
		}
	}
}

func TestStoreFailedMutationPreservesDiskAndMemory(t *testing.T) {
	s := newTestStore(t)
	before, _ := os.ReadFile(s.path)
	want := sampleRecord()
	broken := errors.New("simulated rename failure")
	writer := s.writeFile
	s.writeFile = func(string, []byte) error { return broken }
	if err := s.Put("20", want); !errors.Is(err, broken) {
		t.Fatal("expected write failure")
	}
	if _, ok := s.Get("20"); ok {
		t.Fatal("failed write changed memory")
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed disk")
	}
	s.writeFile = writer
	if err := s.Put("20", want); err != nil {
		t.Fatal(err)
	}
	s.writeFile = func(string, []byte) error { return broken }
	if err := s.Remove("20"); !errors.Is(err, broken) {
		t.Fatal("expected delete failure")
	}
	if got, ok := s.Get("20"); !ok || got != want {
		t.Fatal("failed deletion changed memory")
	}
}

func TestStoreConcurrentRecordsDoNotLoseUpdates(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for _, id := range []string{"20", "21", "22", "23"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := s.Put(id, sampleRecord()); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	if len(s.IDs()) != 4 {
		t.Fatal("lost concurrent update")
	}
	reopened, err := OpenStore(s.path, "1", time.UnixMilli(4000))
	if err != nil || len(reopened.IDs()) != 4 {
		t.Fatalf("lost persisted update: %v", err)
	}
}
