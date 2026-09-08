package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	t0 := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		limit  int
		window time.Duration
		steps  []struct {
			at      time.Duration
			install int64
			want    bool
		}
	}{
		{
			name:   "limit caps one installation within a window",
			limit:  2,
			window: time.Hour,
			steps: []struct {
				at      time.Duration
				install int64
				want    bool
			}{
				{0, 1, true},
				{time.Minute, 1, true},
				{2 * time.Minute, 1, false},
			},
		},
		{
			name:   "window expiry resets the count",
			limit:  1,
			window: time.Hour,
			steps: []struct {
				at      time.Duration
				install int64
				want    bool
			}{
				{0, 1, true},
				{time.Minute, 1, false},
				{61 * time.Minute, 1, true},
			},
		},
		{
			name:   "installations are isolated from each other",
			limit:  1,
			window: time.Hour,
			steps: []struct {
				at      time.Duration
				install int64
				want    bool
			}{
				{0, 1, true},
				{0, 2, true},
				{time.Minute, 1, false},
				{time.Minute, 2, false},
			},
		},
		{
			name:   "zero limit disables limiting",
			limit:  0,
			window: time.Hour,
			steps: []struct {
				at      time.Duration
				install int64
				want    bool
			}{
				{0, 1, true},
				{0, 1, true},
				{0, 1, true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(tt.limit, tt.window)
			now := t0
			l.SetClock(func() time.Time { return now })
			for i, s := range tt.steps {
				now = t0.Add(s.at)
				if got := l.Allow(s.install); got != s.want {
					t.Fatalf("step %d (install %d at +%s): Allow = %v, want %v", i, s.install, s.at, got, s.want)
				}
			}
		})
	}
}
