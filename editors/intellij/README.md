# Modulex for IntelliJ

A client of the local, read-only modulex MCP server (`tools/mcpserver`),
per ADR-0035 plan step 5 (Jira MOD-82). Every action runs against your
local checkout over stdio — no hosted backend, and fully offline while
allow-network stays off (the default; networked checks are then reported
as skipped, never run).

## Actions (Tools → Modulex)

| Action | What it does |
|---|---|
| Review Diff Against Base Ref | `review_diff` between a base ref (default `origin/main`) and `HEAD`; results open as a document |
| Run Focused Verification (Changed Files) | `git status` → `recommend_verification` → runs the focused checks |
| Run Full Verification Gates | Runs the repository's full declared gates |
| Discover Repository | Opens the `discover_repository` report as JSON |
| Read Repository Contract | Opens the parsed `modulex.agent.yaml` (tri-state: absent / invalid / valid) |
| Create Handoff Envelope | `create_handoff` with the most recent results; opens the `provenance.Envelope` as JSON |
| Restart MCP Server | Stops the server; it restarts on the next action |

## Server resolution

1. The configured server command (project property `modulex.server.command`),
   run from the project root.
2. In a modulex checkout: `go run -C tools/mcpserver ./cmd/mcpserver`
   (a nested Go module, so it builds from its own directory).
3. Otherwise the action fails with guidance — build a server binary with
   `go build github.com/mediusfy/modulex/tools/mcpserver/cmd/mcpserver`
   and configure it.

## Development

```sh
gradle test           # core unit tests; no IntelliJ Platform needed to run them
gradle buildPlugin    # full plugin zip (downloads the IntelliJ Platform)
gradle runIde         # launch a sandbox IDE with the plugin
```

The layout mirrors `editors/vscode`: `core/` (`McpClient`, `ToolSurface`,
`Render`, `ServerResolver`) has no IntelliJ imports and is fully
unit-tested; the platform glue (`ModulexService`, `Actions`, `plugin.xml`)
stays thin. The MCP client is hand-rolled (newline-delimited JSON-RPC 2.0
over stdio) so the plugin needs no MCP SDK. The tool surface it drives is
described editor-agnostically in `../tool-surface.json`, bundled verbatim
into the plugin resources at `modulex/tool-surface.json`.
