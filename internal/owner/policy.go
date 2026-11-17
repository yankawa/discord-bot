// Package owner reserves channel management for the audited creator and the bot.
package owner

import (
	"fmt"
	"sort"
	"strconv"
)

const (
	ManageChannels    uint64 = 1 << 4
	ManageRoles       uint64 = 1 << 28
	ViewChannel       uint64 = 1 << 10
	Connect           uint64 = 1 << 20
	ChannelObfuscated uint64 = 1 << 17
	management               = ManageChannels | ManageRoles
)

// Overwrite uses Discord's type values: 0 is a role, 1 is a member.
type Overwrite struct {
	ID    string
	Type  int
	Allow uint64
	Deny  uint64
}

// ValidID accepts only canonical, nonzero decimal uint64 snowflakes.
func ValidID(id string) bool {
	if id == "" || len(id) > 20 || id[0] == '0' {
		return false
	}
	for _, b := range id {
		if b < '0' || b > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0
}

func normalize(overwrites []Overwrite) ([]Overwrite, error) {
	result := append([]Overwrite(nil), overwrites...)
	seen := make(map[string]struct{}, len(result))
	for _, item := range result {
		if !ValidID(item.ID) || (item.Type != 0 && item.Type != 1) {
			return nil, fmt.Errorf("invalid permission overwrite ID/type: %q", item.ID)
		}
		if _, ok := seen[item.ID]; ok {
			return nil, fmt.Errorf("duplicate permission overwrite ID: %s", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// BuildOverwrites preserves all unrelated permission bits. An empty ownerID
// seals channel management while exact audit evidence is unavailable.
// Administrators and the guild owner still bypass channel overwrites in Discord.
func BuildOverwrites(existing []Overwrite, guildID, botID, ownerID string, voice bool) ([]Overwrite, error) {
	if !ValidID(guildID) || !ValidID(botID) || (ownerID != "" && !ValidID(ownerID)) || guildID == botID || guildID == ownerID {
		return nil, fmt.Errorf("invalid guild, bot, or owner ID")
	}
	normalized, err := normalize(existing)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]Overwrite, len(normalized)+3)
	for _, entry := range normalized {
		if (entry.ID == guildID && entry.Type != 0) || ((entry.ID == botID || entry.ID == ownerID) && entry.Type != 1) {
			return nil, fmt.Errorf("incorrect overwrite type for %s", entry.ID)
		}
		entry.Allow &^= management
		entry.Deny |= management
		entries[entry.ID] = entry
	}
	if _, ok := entries[guildID]; !ok {
		entries[guildID] = Overwrite{ID: guildID, Type: 0, Deny: management}
	}
	access := ViewChannel
	if voice {
		access |= Connect
	}
	grant := func(id string, bits uint64) {
		entry, ok := entries[id]
		if !ok {
			entry = Overwrite{ID: id, Type: 1, Deny: management}
		}
		entry.Allow |= bits
		entry.Deny &^= bits
		entries[id] = entry
	}
	if ownerID != "" {
		grant(ownerID, ManageChannels|access)
	}
	grant(botID, management|access)
	result := make([]Overwrite, 0, len(entries))
	for _, item := range entries {
		result = append(result, item)
	}
	return normalize(result)
}

// EqualOverwrites validates both lists and ignores their ordering.
func EqualOverwrites(a, b []Overwrite) (bool, error) {
	x, err := normalize(a)
	if err != nil {
		return false, err
	}
	y, err := normalize(b)
	if err != nil {
		return false, err
	}
	if len(x) != len(y) {
		return false, nil
	}
	for i := range x {
		if x[i] != y[i] {
			return false, nil
		}
	}
	return true, nil
}
