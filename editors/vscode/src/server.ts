// Locates the modulex MCP server for a workspace. Auto-detection covers a
// modulex checkout (go run against the in-repo nested module, which builds
// offline from the local source and module cache); any other repository
// points modulex.server.command at a prebuilt mcpserver binary.

import * as fs from "node:fs";
import * as path from "node:path";
import type { ServerSpec } from "./mcpClient";

export function resolveServerSpec(
  root: string,
  configuredCommand: string[],
): ServerSpec | undefined {
  if (configuredCommand.length > 0) {
    return {
      command: configuredCommand[0],
      args: configuredCommand.slice(1),
      cwd: root,
    };
  }
  const inRepoServer = path.join(
    root,
    "tools",
    "mcpserver",
    "cmd",
    "mcpserver",
  );
  if (fs.existsSync(inRepoServer)) {
    // tools/mcpserver is a nested Go module, so it must be built from its
    // own directory (-C), not from the repository root's module.
    return {
      command: "go",
      args: ["run", "-C", "tools/mcpserver", "./cmd/mcpserver"],
      cwd: root,
    };
  }
  return undefined;
}

export const NO_SERVER_GUIDANCE =
  "No modulex MCP server found. In a modulex checkout it is auto-detected; " +
  "elsewhere set modulex.server.command to a prebuilt mcpserver binary " +
  "(go build github.com/mediusfy/modulex/tools/mcpserver/cmd/mcpserver).";
