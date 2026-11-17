package owner

import (
	"context"
	"fmt"
	"strconv"
)

const ChannelCreateAction = 10

type AuditEntry struct {
	ID         string
	Action     int
	TargetID   string
	ExecutorID string
}

// FindCreateEntry accepts evidence for exactly this channel, never the most
// recent unrelated action. An absent/deleted executor is not ownership proof.
func FindCreateEntry(entries []AuditEntry, channelID string) *AuditEntry {
	if !ValidID(channelID) {
		return nil
	}
	for _, entry := range entries {
		if entry.Action == ChannelCreateAction && entry.TargetID == channelID && ValidID(entry.ID) && ValidID(entry.ExecutorID) {
			copy := entry
			return &copy
		}
	}
	return nil
}

// AuditPageFetcher must request CHANNEL_CREATE (10), limit 100, and optionally
// before. LookupCreateEntry uses audit IDs only as pagination cursors.
type AuditPageFetcher func(ctx context.Context, before string, limit int) ([]AuditEntry, error)

func LookupCreateEntry(ctx context.Context, fetch AuditPageFetcher, channelID string, maxPages int) (*AuditEntry, error) {
	if !ValidID(channelID) || maxPages < 0 || maxPages > 100 {
		return nil, fmt.Errorf("invalid audit lookup parameters")
	}
	before := ""
	var beforeNumber uint64
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := fetch(ctx, before, 100)
		if err != nil {
			return nil, err
		}
		if match := FindCreateEntry(page, channelID); match != nil {
			return match, nil
		}
		if len(page) < 100 {
			return nil, nil
		}
		var oldest uint64 = ^uint64(0)
		for _, entry := range page {
			if !ValidID(entry.ID) {
				return nil, fmt.Errorf("invalid audit pagination ID %q", entry.ID)
			}
			n, _ := strconv.ParseUint(entry.ID, 10, 64)
			if n < oldest {
				oldest = n
			}
		}
		if before != "" && oldest >= beforeNumber {
			return nil, nil
		}
		before, beforeNumber = strconv.FormatUint(oldest, 10), oldest
	}
	return nil, nil
}
