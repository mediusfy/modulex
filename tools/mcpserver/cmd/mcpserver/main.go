// Command mcpserver runs the read-only Modulex MCP server over stdio, for
// a host (e.g. Claude Code) to spawn as a repository-local subprocess. See
// docs/planning/agent-mcp-server-guide.md.
//
// Usage:
//
//	mcpserver
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/tools/mcpserver"
)

func main() {
	if err := mcpserver.NewServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
