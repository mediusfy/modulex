// Command-line splitting for the configured server command. Kept in core
// so it is unit-testable without the platform.
package com.mediusfy.modulex.intellij.core

/**
 * Splits a configured command line into argv, honoring double quotes so
 * paths with spaces work ("C:\Program Files\...\mcpserver.exe" --flag).
 * A naive space split would break every such path.
 */
fun splitCommand(line: String): List<String> {
    val args = mutableListOf<String>()
    val current = StringBuilder()
    var inQuotes = false
    for (c in line) {
        when {
            c == '"' -> inQuotes = !inQuotes
            c == ' ' && !inQuotes -> {
                if (current.isNotEmpty()) {
                    args.add(current.toString())
                    current.clear()
                }
            }
            else -> current.append(c)
        }
    }
    if (current.isNotEmpty()) {
        args.add(current.toString())
    }
    return args
}
