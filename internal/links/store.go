package links

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record stores IDs and a content hash only; original message text is not saved.
// An empty ReplyID is a durable pending send which can be retried safely with
// Discord's enforced nonce (within Discord's nonce deduplication window).
type Record struct {
	MessageID   string `json:"messageId"`
	ChannelID   string `json:"channelId"`
	ReplyID     string `json:"replyId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Generation  uint32 `json:"generation,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type diskState struct {
	Version     int               `json:"version"`
	GuildID     string            `json:"guildId"`
	ActivatedAt int64             `json:"activatedAt"`
	Records     map[string]Record `json:"records"`
}

// Store must be used by only one bot process. Its in-process methods are safe
// for concurrent handlers. Every mutation is committed by an atomic rename.
type Store struct {
	mu    sync.RWMutex
	path  string
	state diskState
	write func(string, []byte) error
}

func OpenStore(path, guildID string, now time.Time) (*Store, error) {
	if path == "" || !validID(guildID) || now.UnixMilli() <= 0 {
		return nil, errors.New("invalid link state path, guild ID or activation time")
	}
	s := &Store{path: path, write: atomicWrite}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = diskState{Version: 1, GuildID: guildID, ActivatedAt: now.UnixMilli(), Records: map[string]Record{}}
		if err := s.persist(s.state); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open link state: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > 32<<20 {
		return nil, errors.New("link state cannot be read or exceeds the 32 MiB size limit")
	}
	dec := json.NewDecoder(io.LimitReader(f, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s.state); err != nil {
		return nil, fmt.Errorf("invalid link state (refusing to reset): %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid link state: trailing data")
	}
	if s.state.Version != 1 || s.state.GuildID != guildID || s.state.ActivatedAt <= 0 || s.state.Records == nil {
		return nil, errors.New("invalid link state version, guild or activation time (refusing to reset)")
	}
	for id, rec := range s.state.Records {
		if id != rec.MessageID {
			return nil, errors.New("invalid link state: record key differs from message ID")
		}
		if err := validateRecord(rec); err != nil {
			return nil, fmt.Errorf("invalid link state record %s: %w", id, err)
		}
	}
	return s, nil
}

func validateRecord(rec Record) error {
	if !validID(rec.MessageID) || !validID(rec.ChannelID) || rec.CreatedAt <= 0 || rec.UpdatedAt < rec.CreatedAt {
		return errors.New("invalid IDs or timestamps")
	}
	if rec.ReplyID != "" && (!validID(rec.ReplyID) || rec.ReplyID == rec.MessageID || rec.ContentHash == "") {
		return errors.New("invalid reply ID or missing content hash")
	}
	if rec.ContentHash != "" {
		hash, err := hex.DecodeString(rec.ContentHash)
		if err != nil || len(hash) != 32 {
			return errors.New("invalid SHA-256 content hash")
		}
	}
	return nil
}

func (s *Store) GuildID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.GuildID
}

func (s *Store) ActivatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.UnixMilli(s.state.ActivatedAt)
}

func (s *Store) Lookup(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.state.Records[id]
	return rec, ok
}

func (s *Store) Records() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Record, 0, len(s.state.Records))
	for _, rec := range s.state.Records {
		result = append(result, rec)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].MessageID < result[j].MessageID })
	return result
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.state.Records)
}

func (s *Store) Put(rec Record) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.copyState()
	next.Records[rec.MessageID] = rec
	if err := s.persist(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.Records[id]; !ok {
		return nil
	}
	next := s.copyState()
	delete(next.Records, id)
	if err := s.persist(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) copyState() diskState {
	next := s.state
	next.Records = make(map[string]Record, len(s.state.Records))
	for k, v := range s.state.Records {
		next.Records[k] = v
	}
	return next
}

func (s *Store) persist(next diskState) error {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := s.write(s.path, append(data, '\n')); err != nil {
		return fmt.Errorf("persist link state: %w", err)
	}
	return nil
}

func atomicWrite(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".links-state-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// The rename is the commit point. Directory sync improves crash durability
	// where supported; failure after commit must not leave memory behind disk.
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
