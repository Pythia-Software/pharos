package archive

import (
	"context"
	"runtime"
	"sync"
)

// Preparation has its own small CPU pool, including when inference uses the
// GPU. Respect the saved thread target and power-adjusted budget. Limited runs
// stay serial: their next conversation depends on the preceding message count.
func labelerPreparationWorkers(settings LabelerSettings, limit int, serial bool) int {
	if limit > 0 || serial {
		return 1
	}
	workers := min(4, runtime.GOMAXPROCS(0), max(1, settings.Threads))
	if settings.ThreadBudget > 0 {
		workers = min(workers, settings.ThreadBudget)
	}
	return workers
}

// prepareConversations prepares at most workers conversations ahead, then
// delivers them in their original order. Each worker owns its mutable builder.
// A slow first conversation cannot accumulate an unbounded reorder queue, and
// a blocked consumer stops dispatch (including while acquiring row credits).
// Returning false from consume stops dispatch and joins all workers.
func prepareConversations[T any](parent context.Context, count, workers int, prepare func(context.Context, int, int) T, consume func(int, T) bool) error {
	ctx, cancel := context.WithCancel(parent)
	workers = min(count, max(1, workers))
	type result struct {
		index int
		value T
	}
	tasks := make(chan int, workers)
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index := <-tasks:
					if ctx.Err() != nil {
						return
					}
					value := prepare(ctx, worker, index)
					select {
					case results <- result{index, value}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() { cancel(); wg.Wait() }()
	for index := range workers {
		tasks <- index
	}
	next := workers
	pending := make(map[int]T, workers)
	for index := 0; index < count; index++ {
		value, ready := pending[index]
		for !ready {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case result := <-results:
				pending[result.index] = result.value
			}
			value, ready = pending[index]
		}
		delete(pending, index)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !consume(index, value) {
			return parent.Err()
		}
		if next < count {
			tasks <- next
			next++
		}
	}
	return parent.Err()
}
