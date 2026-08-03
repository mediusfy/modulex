package mcpserver

import "testing"

func TestDiscoverRepository(t *testing.T) {
	t.Run("valid root with a go.mod", func(t *testing.T) {
		out, err := discoverRepository("../..")
		if err != nil {
			t.Fatalf("discoverRepository() error = %v", err)
		}
		if len(out.Repository.Modules) == 0 {
			t.Error("Repository.Modules is empty, want at least the root module")
		}
	})

	t.Run("nonexistent root", func(t *testing.T) {
		_, err := discoverRepository("/does/not/exist/at/all")
		if err == nil {
			t.Fatal("discoverRepository() error = nil, want an error for a nonexistent root")
		}
	})

	t.Run("empty root defaults to dot", func(t *testing.T) {
		out, err := discoverRepository("")
		if err != nil {
			t.Fatalf("discoverRepository() error = %v", err)
		}
		if out.Repository.Root == "" {
			t.Error("Repository.Root is empty, want the resolved absolute path of \".\"")
		}
	})
}
