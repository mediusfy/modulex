package busshutdown

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestWait covers both outcomes Wait distinguishes: done closing first
// (nil) and ctx expiring first (a wrapped ctx.Err()), as rows of the same
// shape so a third outcome is a new row, not a new function.
func TestWait(t *testing.T) {
	tests := []struct {
		name       string
		ctxTimeout time.Duration // 0 means an already-canceled ctx
		doneDelay  time.Duration // how long before done closes; <0 means never
		wantErr    bool
	}{
		{
			name:       "done closes before ctx expires",
			ctxTimeout: 50 * time.Millisecond,
			doneDelay:  0,
			wantErr:    false,
		},
		{
			name:       "ctx expires before done ever closes",
			ctxTimeout: 10 * time.Millisecond,
			doneDelay:  -1,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.ctxTimeout)
			defer cancel()

			done := make(chan struct{})
			if tt.doneDelay >= 0 {
				close(done)
			}

			err := Wait(ctx, done, "test-adapter")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Wait() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("Wait() error = %v, want it to wrap context.DeadlineExceeded", err)
				}
			}
		})
	}
}

// TestWaitGroup covers the two outcomes WaitGroup distinguishes, mirroring
// TestWait but driving a *sync.WaitGroup instead of a done channel
// directly.
func TestWaitGroup(t *testing.T) {
	tests := []struct {
		name       string
		ctxTimeout time.Duration
		doneWg     bool // whether the WaitGroup finishes before ctx expires
		wantErr    bool
	}{
		{
			name:       "wg finishes before ctx expires",
			ctxTimeout: 50 * time.Millisecond,
			doneWg:     true,
			wantErr:    false,
		},
		{
			name:       "ctx expires before the wg ever finishes",
			ctxTimeout: 10 * time.Millisecond,
			doneWg:     false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.ctxTimeout)
			defer cancel()

			var wg sync.WaitGroup
			wg.Add(1)
			if tt.doneWg {
				wg.Done()
			} else {
				defer wg.Done() // release the goroutine WaitGroup spawns, after the test observes the timeout
			}

			err := WaitGroup(ctx, &wg, "test-adapter")
			if (err != nil) != tt.wantErr {
				t.Fatalf("WaitGroup() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("WaitGroup() error = %v, want it to wrap context.DeadlineExceeded", err)
			}
		})
	}
}
