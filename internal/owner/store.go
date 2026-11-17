package owner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record contains only immutable creation evidence and operational timestamps.
type Record struct {
	CategoryID       string
	CreatedAt        int64
	OwnerID          string
	AuditID          string
	LastAppliedAt    int64
	UnresolvedLogged bool
}

// Pointer fields keep the original Node bot's version-1 state representation.
type diskRecord struct {
	CategoryID       string  `json:"categoryId"`
	CreatedAt        int64   `json:"createdAt"`
	OwnerID          *string `json:"ownerId"`
	AuditID          *string `json:"auditId"`
	LastAppliedAt    *int64  `json:"lastAppliedAt"`
	UnresolvedLogged bool    `json:"unresolvedLogged"`
}

func (record *diskRecord) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"categoryId", "createdAt", "ownerId", "auditId", "lastAppliedAt", "unresolvedLogged"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("ownership record is missing %s", name)
		}
	}
	if bytes.Equal(bytes.TrimSpace(fields["unresolvedLogged"]), []byte("null")) {
		return fmt.Errorf("ownership record unresolvedLogged must be a boolean")
	}
	// An alias avoids recursing into this method while still rejecting unknown
	// fields. Missing/null owner and audit evidence cannot be silently invented.
	type plainRecord diskRecord
	var parsed plainRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*record = diskRecord(parsed)
	return nil
}

type diskState struct {
	Version     int                   `json:"version"`
	GuildID     string                `json:"guildId"`
	ActivatedAt int64                 `json:"activatedAt"`
	Channels    map[string]diskRecord `json:"channels"`
}

// Store serializes concurrent updates, writes a synced temporary file, and
// commits with an atomic rename. One process must own a state file at a time.
type Store struct {
	mu            sync.RWMutex
	path, guildID string
	activatedAt   int64
	records       map[string]Record
	writeFile     func(string, []byte) error
}

func OpenStore(path, guildID string, now time.Time) (*Store, error) {
	if path == "" || !ValidID(guildID) || now.UnixMilli() <= 0 {
		return nil, fmt.Errorf("invalid state path, guild ID, or activation time")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: absolute, guildID: guildID, activatedAt: now.UnixMilli(), records: map[string]Record{}, writeFile: atomicWrite}
	raw, err := os.ReadFile(absolute)
	if os.IsNotExist(err) {
		if err := s.flush(s.records); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read owner state: %w", err)
	}
	var state diskState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("invalid owner state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("owner state has trailing JSON")
	}
	if state.Version != 1 || state.GuildID != guildID || state.ActivatedAt <= 0 || state.Channels == nil {
		return nil, fmt.Errorf("invalid owner state or GUILD_ID differs from its saved value")
	}
	s.activatedAt = state.ActivatedAt
	for id, d := range state.Channels {
		r := Record{CategoryID: d.CategoryID, CreatedAt: d.CreatedAt, UnresolvedLogged: d.UnresolvedLogged}
		if d.OwnerID != nil {
			r.OwnerID = *d.OwnerID
			if r.OwnerID == "" {
				return nil, fmt.Errorf("invalid empty owner ID: %s", id)
			}
		}
		if d.AuditID != nil {
			r.AuditID = *d.AuditID
			if r.AuditID == "" {
				return nil, fmt.Errorf("invalid empty audit ID: %s", id)
			}
		}
		if d.LastAppliedAt != nil {
			r.LastAppliedAt = *d.LastAppliedAt
			if r.LastAppliedAt <= 0 {
				return nil, fmt.Errorf("invalid application time: %s", id)
			}
		}
		if err := validateRecord(id, r, guildID); err != nil {
			return nil, err
		}
		s.records[id] = r
	}
	return s, nil
}

func validateRecord(id string, r Record, guildID string) error {
	if !ValidID(id) || !ValidID(r.CategoryID) || r.CreatedAt <= 0 || r.LastAppliedAt < 0 || (r.OwnerID == "") != (r.AuditID == "") || (r.OwnerID != "" && (!ValidID(r.OwnerID) || r.OwnerID == guildID || !ValidID(r.AuditID))) {
		return fmt.Errorf("invalid ownership record: %s", id)
	}
	return nil
}

func (s *Store) ActivatedAt() int64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.activatedAt }
func (s *Store) GuildID() string    { return s.guildID }
func (s *Store) Get(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	return r, ok
}
func (s *Store) IDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Store) Put(id string, r Record) error {
	if err := validateRecord(id, r, s.guildID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[id]; ok && existing == r {
		return nil
	}
	next := copyRecords(s.records)
	next[id] = r
	if err := s.flush(next); err != nil {
		return err
	}
	s.records = next
	return nil
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return nil
	}
	next := copyRecords(s.records)
	delete(next, id)
	if err := s.flush(next); err != nil {
		return err
	}
	s.records = next
	return nil
}

func copyRecords(records map[string]Record) map[string]Record {
	result := make(map[string]Record, len(records))
	for id, r := range records {
		result[id] = r
	}
	return result
}

func (s *Store) flush(records map[string]Record) error {
	state := diskState{Version: 1, GuildID: s.guildID, ActivatedAt: s.activatedAt, Channels: make(map[string]diskRecord, len(records))}
	for id, r := range records {
		if err := validateRecord(id, r, s.guildID); err != nil {
			return err
		}
		d := diskRecord{CategoryID: r.CategoryID, CreatedAt: r.CreatedAt, UnresolvedLogged: r.UnresolvedLogged}
		if r.OwnerID != "" {
			ownerID, auditID := r.OwnerID, r.AuditID
			d.OwnerID, d.AuditID = &ownerID, &auditID
		}
		if r.LastAppliedAt != 0 {
			last := r.LastAppliedAt
			d.LastAppliedAt = &last
		}
		state.Channels[id] = d
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := s.writeFile(s.path, raw); err != nil {
		return fmt.Errorf("persist owner state: %w", err)
	}
	return nil
}

func atomicWrite(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".owner-state-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	// The rename is the commit point. Directory fsync is best-effort because
	// some operating systems/filesystems do not support it; never claim that
	// a post-commit fsync error preserved the previous file.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
