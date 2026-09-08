// Minimal MCP client over the stdio transport: newline-delimited JSON-RPC
// 2.0, matching the Go SDK's mcp.StdioTransport that tools/mcpserver runs.
// Hand-rolled (like editors/vscode/src/mcpClient.ts) so the plugin needs no
// MCP SDK and works fully offline.
//
// The client is synchronous and stream-based: requests are serialized under
// a lock (the plugin issues one tool call at a time from a background
// task), and the transport is a plain Reader/Writer pair so tests drive it
// with in-memory pipes — no subprocess required.
package com.mediusfy.modulex.intellij.core

import com.google.gson.Gson
import com.google.gson.JsonObject
import com.google.gson.JsonParser
import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.File

class McpException(message: String) : Exception(message)

interface McpTransport {
    val reader: BufferedReader
    val writer: BufferedWriter

    fun close()
}

data class ServerSpec(val command: List<String>, val workingDirectory: File)

private class ProcessTransport(private val process: Process, onServerLog: (String) -> Unit) :
    McpTransport {
    override val reader: BufferedReader = process.inputStream.bufferedReader()
    override val writer: BufferedWriter = process.outputStream.bufferedWriter()

    init {
        Thread {
            process.errorStream.bufferedReader().forEachLine(onServerLog)
        }.apply {
            isDaemon = true
            name = "modulex-mcp-stderr"
            start()
        }
    }

    override fun close() {
        process.destroy()
    }
}

class McpClient(private val transport: McpTransport) {
    private val gson = Gson()
    private var nextId = 1
    private val lock = Any()

    companion object {
        const val PROTOCOL_VERSION = "2024-11-05"

        fun start(spec: ServerSpec, onServerLog: (String) -> Unit): McpClient {
            val process =
                try {
                    ProcessBuilder(spec.command).directory(spec.workingDirectory).start()
                } catch (e: Exception) {
                    throw McpException("modulex MCP server failed to start: ${e.message}")
                }
            val client = McpClient(ProcessTransport(process, onServerLog))
            client.initialize()
            return client
        }
    }

    fun initialize() {
        val params = JsonObject()
        params.addProperty("protocolVersion", PROTOCOL_VERSION)
        params.add("capabilities", JsonObject())
        val clientInfo = JsonObject()
        clientInfo.addProperty("name", "modulex-intellij")
        clientInfo.addProperty("version", "0.1.0")
        params.add("clientInfo", clientInfo)
        request("initialize", params)
        notify("notifications/initialized", JsonObject())
    }

    fun callTool(name: String, args: JsonObject): JsonObject {
        val params = JsonObject()
        params.addProperty("name", name)
        params.add("arguments", args)
        val result = request("tools/call", params)
        val text =
            (result.getAsJsonArray("content") ?: com.google.gson.JsonArray())
                .filterIsInstance<JsonObject>()
                .filter { it.get("type")?.asString == "text" }
                .mapNotNull { it.get("text")?.asString }
                .joinToString("\n")
        if (result.get("isError")?.asBoolean == true) {
            throw McpException(text.ifEmpty { "tool $name failed" })
        }
        result.get("structuredContent")?.let {
            if (it.isJsonObject) {
                return it.asJsonObject
            }
        }
        return try {
            JsonParser.parseString(text).asJsonObject
        } catch (_: Exception) {
            throw McpException("tool $name returned no structured content and non-JSON text: $text")
        }
    }

    private fun request(method: String, params: JsonObject): JsonObject =
        synchronized(lock) {
            val id = nextId++
            writeMessage(id, method, params)
            // Read until this request's response; the server may interleave
            // notifications (no id), which are skipped.
            while (true) {
                val line = transport.reader.readLine() ?: throw McpException(
                    "modulex MCP server closed the connection",
                )
                if (line.isBlank()) {
                    continue
                }
                val msg =
                    try {
                        JsonParser.parseString(line).asJsonObject
                    } catch (_: Exception) {
                        continue // not JSON-RPC; ignore, matching the VSCode client
                    }
                if (msg.get("id")?.takeIf { it.isJsonPrimitive }?.asInt != id) {
                    continue
                }
                msg.getAsJsonObject("error")?.let {
                    throw McpException(it.get("message")?.asString ?: "JSON-RPC error")
                }
                return msg.getAsJsonObject("result") ?: JsonObject()
            }
            @Suppress("UNREACHABLE_CODE")
            throw IllegalStateException("unreachable")
        }

    private fun notify(method: String, params: JsonObject) {
        synchronized(lock) { writeMessage(null, method, params) }
    }

    private fun writeMessage(id: Int?, method: String, params: JsonObject) {
        val msg = JsonObject()
        msg.addProperty("jsonrpc", "2.0")
        if (id != null) {
            msg.addProperty("id", id)
        }
        msg.addProperty("method", method)
        msg.add("params", params)
        transport.writer.write(gson.toJson(msg))
        transport.writer.write("\n")
        transport.writer.flush()
    }

    fun dispose() {
        transport.close()
    }
}
