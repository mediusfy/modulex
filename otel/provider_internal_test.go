package otel

import "testing"

// TestSanitizeEndpoint_SplitsPath locks in that a configured OTLP endpoint
// with a URL path component (e.g. "http://host:4318/v1") has that path split
// out rather than left attached to the host. A prior version returned
// "host:4318/v1" as the hostPort value, which the OTLP HTTP exporter then
// tried to resolve literally as a host, including the "/v1" suffix.
func TestSanitizeEndpoint_SplitsPath(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		wantHostPort string
		wantPath     string
		wantIsHTTP   bool
	}{
		{"http with path", "http://host:4318/v1", "host:4318", "/v1", true},
		{"http with deeper path", "http://host:4318/v1/traces", "host:4318", "/v1/traces", true},
		{"http without path", "http://host:4318", "host:4318", "", true},
		{"https with path", "https://host:4318/v1", "host:4318", "/v1", false},
		{"bare host:port, no scheme, no path", "host:4317", "host:4317", "", false},
		{"empty endpoint", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHostPort, gotPath, gotIsHTTP := sanitizeEndpoint(tt.endpoint)
			if gotHostPort != tt.wantHostPort {
				t.Errorf("sanitizeEndpoint(%q) hostPort = %q, want %q", tt.endpoint, gotHostPort, tt.wantHostPort)
			}
			if gotPath != tt.wantPath {
				t.Errorf("sanitizeEndpoint(%q) path = %q, want %q", tt.endpoint, gotPath, tt.wantPath)
			}
			if gotIsHTTP != tt.wantIsHTTP {
				t.Errorf("sanitizeEndpoint(%q) isHTTP = %v, want %v", tt.endpoint, gotIsHTTP, tt.wantIsHTTP)
			}
		})
	}
}

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
