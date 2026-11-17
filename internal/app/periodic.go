package app

import (
	"context"
	"sync"
	"time"
)

// periodic coalesces reconnect triggers and never overlaps scans.
func periodic(ctx context.Context, interval time.Duration, trigger <-chan struct{}, scan func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan(ctx)
		case <-trigger:
			scan(ctx)
		}
	}
}

func triggerScan(trigger chan<- struct{}) {
	select {
	case trigger <- struct{}{}:
	default:
	}
}

func withSlot(ctx context.Context, slots chan struct{}, run func()) {
	select {
	case <-ctx.Done():
		return
	case slots <- struct{}{}:
		defer func() { <-slots }()
		run()
	}
}

func parallel(ctx context.Context, workers int, ids []string, process func(context.Context, string)) {
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id, ok := <-jobs:
					if !ok {
						return
					}
					process(ctx, id)
				}
			}
		}()
	}
send:
	for _, id := range ids {
		select {
		case <-ctx.Done():
			break send
		case jobs <- id:
		}
	}
	close(jobs)
	wg.Wait()
}
