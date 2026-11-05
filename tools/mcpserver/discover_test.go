package mcpserver

import "testing"

func TestDiscoverRepository(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "valid root with a go.mod",
			run: func(t *testing.T) {
				out, err := discoverRepository("../..")
				if err != nil {
					t.Fatalf("discoverRepository() error = %v", err)
				}
				if len(out.Repository.Modules) == 0 {
					t.Error("Repository.Modules is empty, want at least the root module")
				}
			},
		},
		{
			name: "nonexistent root",
			run: func(t *testing.T) {
				_, err := discoverRepository("/does/not/exist/at/all")
				if err == nil {
					t.Fatal("discoverRepository() error = nil, want an error for a nonexistent root")
				}
			},
		},
		{
			name: "empty root defaults to dot",
			run: func(t *testing.T) {
				out, err := discoverRepository("")
				if err != nil {
					t.Fatalf("discoverRepository() error = %v", err)
				}
				if out.Repository.Root == "" {
					t.Error("Repository.Root is empty, want the resolved absolute path of \".\"")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
