package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/discovery"
)

// DiscoverRepositoryIn is discover_repository's input.
type DiscoverRepositoryIn struct {
	// Root is the repository root to scan. Defaults to "." (the server
	// process's working directory) if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root to scan; defaults to \".\" (the server's working directory) if empty"`
}

// DiscoverRepositoryOut is discover_repository's output.
type DiscoverRepositoryOut struct {
	Repository discovery.Repository `json:"repository"`
}

// discoverRepository wraps discovery.Discover. A discovery.Discover error
// (an invalid or inaccessible root path — a genuine caller mistake, not a
// normal "nothing found" outcome) is returned as a real error so the MCP
// client sees a tool-error result rather than an empty-looking Repository.
func discoverRepository(root string) (DiscoverRepositoryOut, error) {
	repo, err := discovery.Discover(resolveRoot(root))
	if err != nil {
		return DiscoverRepositoryOut{}, fmt.Errorf("discover_repository: %w", err)
	}
	return DiscoverRepositoryOut{Repository: repo}, nil
}

func discoverRepositoryHandler(_ context.Context, _ *mcp.CallToolRequest, in DiscoverRepositoryIn) (*mcp.CallToolResult, DiscoverRepositoryOut, error) {
	out, err := discoverRepository(in.Root)
	return nil, out, err
}
