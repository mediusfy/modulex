package otel

import "testing"

// TestIsLoopbackHost_IPv6 locks in that a bracketed IPv6 loopback endpoint
// is recognized the same way its IPv4/hostname equivalents are. A prior
// version split on the last ':' without stripping IPv6 brackets, so
// "[::1]:4317" produced host "[::1]" — which never matched the bare "::1"
// literal — silently missing the auto-insecure default for any installation
// reachable only over IPv6 loopback.
func TestIsLoopbackHost_IPv6(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"bracketed IPv6 loopback with port", "[::1]:4317", true},
		{"bare IPv6 loopback, no port", "::1", true},
		{"IPv4 loopback with port", "127.0.0.1:4317", true},
		{"localhost with port", "localhost:4317", true},
		{"non-loopback IPv6 host with port", "[2001:db8::1]:4317", false},
		{"non-loopback host with port", "collector.internal:4317", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoopbackHost(tt.endpoint); got != tt.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tt.endpoint, got, tt.want)
			}
		})
	}
}
