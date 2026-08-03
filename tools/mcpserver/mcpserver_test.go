package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServer_ListsAllToolsAndCallsDiscoverRepository connects a real MCP
// client to NewServer() over an in-memory transport pair and exercises the
// wire protocol end to end: tools/list, then a tools/call for
// discover_repository — proving the server actually registers and serves
// all six tools, not just that the underlying pure functions work in
// isolation (see discover_test.go etc. for those).
func TestServer_ListsAllToolsAndCallsDiscoverRepository(t *testing.T) {
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := NewServer()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	toolsResult, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	wantNames := map[string]bool{
		"discover_repository":    false,
		"read_contract":          false,
		"recommend_verification": false,
		"run_verification":       false,
		"review_diff":            false,
		"create_handoff":         false,
	}
	for _, tool := range toolsResult.Tools {
		if _, ok := wantNames[tool.Name]; ok {
			wantNames[tool.Name] = true
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("tool %q was not listed by ListTools", name)
		}
	}

	callResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "discover_repository",
		Arguments: map[string]any{"root": "../.."},
	})
	if err != nil {
		t.Fatalf("CallTool(discover_repository) error = %v", err)
	}
	if callResult.IsError {
		t.Fatalf("CallTool(discover_repository) IsError = true, content: %+v", callResult.Content)
	}

	b, err := json.Marshal(callResult.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var out DiscoverRepositoryOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal StructuredContent into DiscoverRepositoryOut: %v", err)
	}
	if len(out.Repository.Modules) == 0 {
		t.Error("Repository.Modules is empty, want at least the root module")
	}
}
