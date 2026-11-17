package links

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testTime = time.Unix(1800000000, 0)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "data", "state.json"), "100", testTime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testRecord(id string) Record {
	return Record{MessageID: id, ChannelID: "200", ReplyID: "900", ContentHash: contentHash("body"), CreatedAt: testTime.UnixMilli(), UpdatedAt: testTime.UnixMilli()}
}

func TestStorePersistsAndRestores(t *testing.T) {
	s := newTestStore(t)
	rec := testRecord("300")
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path, "100", testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.Lookup("300"); !ok || got != rec {
		t.Fatalf("restored = %#v, %v", got, ok)
	}
	if !reopened.ActivatedAt().Equal(testTime) {
		t.Fatal("activation time was reset")
	}
	if info, err := os.Stat(s.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %v, error %v", info, err)
	}
	if err := reopened.Remove("300"); err != nil {
		t.Fatal(err)
	}
	if reopened.Count() != 0 {
		t.Fatal("record survived removal")
	}
}

func TestStoreRefusesCorruptOrWrongGuild(t *testing.T) {
	s := newTestStore(t)
	if _, err := OpenStore(s.path, "999", testTime); err == nil {
		t.Fatal("accepted wrong guild")
	}
	bad := []string{
		`{"version":1`,
		`{"version":2,"guildId":"100","activatedAt":1,"records":{}}`,
		`{"version":1,"guildId":"100","activatedAt":0,"records":{}}`,
		`{"version":1,"guildId":"100","activatedAt":1,"records":null}`,
		`{"version":1,"guildId":"100","activatedAt":1,"records":{},"unknown":true}`,
		`{"version":1,"guildId":"100","activatedAt":1,"records":{}} {}`,
		`{"version":1,"guildId":"100","activatedAt":1,"records":{"300":{"messageId":"301","channelId":"200","createdAt":1,"updatedAt":1}}}`,
		`{"version":1,"guildId":"100","activatedAt":1,"records":{"300":{"messageId":"300","channelId":"200","replyId":"900","createdAt":1,"updatedAt":1}}}`,
	}
	for _, data := range bad {
		if err := os.WriteFile(s.path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(s.path, "100", testTime); err == nil {
			t.Fatalf("accepted invalid state: %s", data)
		}
		got, _ := os.ReadFile(s.path)
		if string(got) != data {
			t.Fatal("corrupt state was silently replaced")
		}
	}
}

func TestStoreWriteFailurePreservesMemoryAndDisk(t *testing.T) {
	s := newTestStore(t)
	initial := testRecord("300")
	if err := s.Put(initial); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.path)
	s.write = func(string, []byte) error { return errors.New("disk full") }
	changed := initial
	changed.ReplyID = "901"
	if err := s.Put(changed); err == nil {
		t.Fatal("expected write failure")
	}
	if got, _ := s.Lookup("300"); got != initial {
		t.Fatal("memory changed before commit")
	}
	if err := s.Remove("300"); err == nil {
		t.Fatal("expected remove failure")
	}
	if !strings.Contains(string(before), "900") || s.Count() != 1 {
		t.Fatal("original state missing")
	}
	after, _ := os.ReadFile(s.path)
	if string(after) != string(before) {
		t.Fatal("disk changed after failed transaction")
	}
}

func TestStoreRejectsInvalidRecords(t *testing.T) {
	tests := []Record{
		{MessageID: "0", ChannelID: "200", CreatedAt: 1, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "x", CreatedAt: 1, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "200", CreatedAt: 0, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "200", CreatedAt: 2, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "200", ReplyID: "300", ContentHash: contentHash("x"), CreatedAt: 1, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "200", ReplyID: "900", CreatedAt: 1, UpdatedAt: 1},
		{MessageID: "300", ChannelID: "200", ContentHash: "bad", CreatedAt: 1, UpdatedAt: 1},
	}
	s := newTestStore(t)
	for _, rec := range tests {
		if err := s.Put(rec); err == nil {
			t.Fatalf("accepted invalid record %#v", rec)
		}
	}
}

func TestAtomicWriteFailureLeavesTargetAndNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing-directory")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("data")); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("temporary file leak: %#v, %v", entries, err)
	}
}
