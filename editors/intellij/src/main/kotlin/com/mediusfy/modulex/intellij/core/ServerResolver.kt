// Locates the modulex MCP server for a project root — the same resolution
// rules as editors/vscode/src/server.ts: an explicit configured command
// wins; a modulex checkout auto-detects go run against the nested module;
// anything else needs configuration.
package com.mediusfy.modulex.intellij.core

import java.io.File

const val NO_SERVER_GUIDANCE =
    "No modulex MCP server found. In a modulex checkout it is auto-detected; " +
        "elsewhere set the Modulex server command to a prebuilt mcpserver binary " +
        "(go build github.com/mediusfy/modulex/tools/mcpserver/cmd/mcpserver)."

fun resolveServerSpec(root: File, configuredCommand: List<String>): ServerSpec? {
    if (configuredCommand.isNotEmpty()) {
        return ServerSpec(configuredCommand, root)
    }
    // tools/mcpserver is a nested Go module, so it must be built from its
    // own directory (-C), not from the repository root's module.
    if (root.resolve("tools/mcpserver/cmd/mcpserver").exists()) {
        return ServerSpec(listOf("go", "run", "-C", "tools/mcpserver", "./cmd/mcpserver"), root)
    }
    return null
}
