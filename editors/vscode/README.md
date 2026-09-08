# Modulex for VSCode

A client of the local, read-only modulex MCP server (`tools/mcpserver`),
per ADR-0035 plan step 5 (Jira MOD-81). Every command runs against your
local checkout over stdio — no hosted backend, and fully offline while
`modulex.allowNetwork` stays `false` (the default; networked checks are
then reported as skipped, never run).

## Commands (Command Palette → "Modulex")

| Command | What it does |
|---|---|
| Review Diff Against Base Ref | `review_diff` between a base ref (default `origin/main`) and `HEAD`; results in a panel |
| Run Focused Verification (changed files) | `git status` → `recommend_verification` → runs the focused checks |
| Run Full Verification Gates | Runs the repository's full declared gates |
| Discover Repository | Opens the `discover_repository` report as JSON |
| Read Repository Contract | Opens the parsed `modulex.agent.yaml` (tri-state: absent / invalid / valid) |
| Create Handoff Envelope | `create_handoff` with the most recent results; opens the `provenance.Envelope` as JSON |
| Restart MCP Server | Stops the server; it restarts on the next command |

## Server resolution

1. `modulex.server.command` (argv array), run from the workspace root.
2. In a modulex checkout: `go run -C tools/mcpserver ./cmd/mcpserver`
   (a nested Go module, so it builds from its own directory).
3. Otherwise the command fails with guidance — build a server binary with
   `go build github.com/mediusfy/modulex/tools/mcpserver/cmd/mcpserver`
   and point the setting at it.

## Development

```sh
npm install        # dev dependencies only; the extension has no runtime deps
npm run compile
npm test           # node:test — no test framework dependency
```

The MCP client is hand-rolled (newline-delimited JSON-RPC 2.0 over stdio,
`src/mcpClient.ts`) so the extension ships with zero runtime dependencies.
The tool surface it drives is described editor-agnostically in
`../tool-surface.json`, shared with the IntelliJ plugin (MOD-82);
`src/toolSurface.ts` is its typed mirror.
