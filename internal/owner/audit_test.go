package owner

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestAuditRequiresExactChannelAndKnownExecutor(t *testing.T) {
	entries := []AuditEntry{
		{ID: "100", Action: 10, TargetID: "20", ExecutorID: "3"},
		{ID: "99", Action: 11, TargetID: "10", ExecutorID: "3"},
		{ID: "98", Action: 10, TargetID: "10"},
		{ID: "97", Action: 10, TargetID: "10", ExecutorID: "4"},
	}
	match := FindCreateEntry(entries, "10")
	if match == nil || match.ID != "97" {
		t.Fatalf("wrong audit match: %+v", match)
	}
	if FindCreateEntry(entries, "30") != nil {
		t.Fatal("guessed unrelated owner")
	}
}

func TestAuditPaginatesUsingOldestNumericID(t *testing.T) {
	calls := 0
	match, err := LookupCreateEntry(context.Background(), func(ctx context.Context, before string, limit int) ([]AuditEntry, error) {
		calls++
		if limit != 100 {
			t.Fatal("unexpected page size")
		}
		if calls == 1 {
			if before != "" {
				t.Fatal("first cursor set")
			}
			page := make([]AuditEntry, 100)
			for i := range page {
				page[i] = AuditEntry{ID: fmt.Sprint(900 + i), Action: 10, TargetID: "20", ExecutorID: "3"}
			}
			return page, nil
		}
		if before != "900" {
			t.Fatalf("wrong cursor %s", before)
		}
		return []AuditEntry{{ID: "850", Action: 10, TargetID: "10", ExecutorID: "4"}}, nil
	}, "10", 5)
	if err != nil || match == nil || match.ExecutorID != "4" || calls != 2 {
		t.Fatalf("match=%+v calls=%d err=%v", match, calls, err)
	}
}

func TestAuditStopsOnNonadvancingCursorAndPropagatesErrors(t *testing.T) {
	calls := 0
	_, err := LookupCreateEntry(context.Background(), func(ctx context.Context, before string, limit int) ([]AuditEntry, error) {
		calls++
		page := make([]AuditEntry, 100)
		for i := range page {
			page[i] = AuditEntry{ID: fmt.Sprint(100 + i), Action: 10, TargetID: "20", ExecutorID: "3"}
		}
		return page, nil
	}, "10", 5)
	if err != nil || calls != 2 {
		t.Fatalf("did not stop: calls=%d err=%v", calls, err)
	}
	want := errors.New("permission denied")
	_, err = LookupCreateEntry(context.Background(), func(context.Context, string, int) ([]AuditEntry, error) { return nil, want }, "10", 5)
	if !errors.Is(err, want) {
		t.Fatalf("lost error %v", err)
	}
}

func TestAuditHonorsMaximumPagesAndCancellation(t *testing.T) {
	calls := 0
	_, err := LookupCreateEntry(context.Background(), func(context.Context, string, int) ([]AuditEntry, error) { calls++; return nil, nil }, "10", 0)
	if err != nil || calls != 0 {
		t.Fatal("zero page limit made a request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = LookupCreateEntry(ctx, func(context.Context, string, int) ([]AuditEntry, error) { calls++; return nil, nil }, "10", 5)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancellation ignored")
	}
}
