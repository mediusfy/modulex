package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessorBoundsConcurrencyAndReportsStats(t *testing.T) {
	p, err := New(Options{Workers: 2, QueueCapacity: 4})
	if err != nil {
		t.Fatal(err)
	}

	var active, maxActive atomic.Int32
	var handles []*Handle
	for i := 0; i < 6; i++ {
		h, err := p.Submit(context.Background(), func(context.Context) error {
			current := active.Add(1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	for _, h := range handles {
		if err := h.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("maximum concurrency = %d, want at most 2", got)
	}
	stats := p.Stats()
	if stats.Accepted != 6 || stats.Completed != 6 || stats.Failed != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestProcessorSubmitBackpressuresAndHonorsContext(t *testing.T) {
	p, err := New(Options{Workers: 1, QueueCapacity: 0})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	h, err := p.Submit(context.Background(), func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Submit(ctx, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorRecoversPanicsAndRejectsAfterClose(t *testing.T) {
	p, err := New(Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Submit(context.Background(), func(context.Context) error { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(context.Background()); !errors.Is(err, ErrPanic) {
		t.Fatalf("panic error = %v, want ErrPanic", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Submit(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit after Close error = %v, want ErrClosed", err)
	}
}

func TestProcessorCloseDrainsAcceptedTasks(t *testing.T) {
	p, err := New(Options{Workers: 2, QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	completed := 0
	for i := 0; i < 4; i++ {
		if _, err := p.Submit(context.Background(), func(context.Context) error {
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			completed++
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if completed != 4 {
		t.Fatalf("completed = %d, want 4", completed)
	}
}
