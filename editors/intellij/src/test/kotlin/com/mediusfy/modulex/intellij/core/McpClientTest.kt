package com.mediusfy.modulex.intellij.core

import com.google.gson.JsonObject
import com.google.gson.JsonParser
import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.PipedInputStream
import java.io.PipedOutputStream
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

/**
 * In-memory transport backed by a fake MCP server thread speaking
 * newline-delimited JSON-RPC, with one canned behavior per tool name, so
 * each table row below exercises one client path — mirroring
 * editors/vscode/test/mcpClient.test.ts.
 */
private class FakeTransport : McpTransport {
    private val toClient = PipedOutputStream()
    private val fromServer = PipedInputStream(toClient)
    private val toServer = PipedOutputStream()
    private val fromClient = PipedInputStream(toServer)

    override val reader: BufferedReader = fromServer.bufferedReader()
    override val writer: BufferedWriter = toServer.bufferedWriter()

    init {
        Thread {
            val serverIn = fromClient.bufferedReader()
            val serverOut = toClient.bufferedWriter()
            fun send(json: String) {
                serverOut.write(json)
                serverOut.write("\n")
                serverOut.flush()
            }
            serverIn.forEachLine { line ->
                val msg = JsonParser.parseString(line).asJsonObject
                val id = msg.get("id")?.takeIf { it.isJsonPrimitive }?.asInt ?: return@forEachLine
                when (msg.get("method").asString) {
                    "initialize" ->
                        send("""{"jsonrpc":"2.0","id":$id,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake"}}}""")
                    "tools/call" ->
                        when (msg.getAsJsonObject("params").get("name").asString) {
                            "structured" -> {
                                // A notification the client must skip while
                                // waiting for its response.
                                send("""{"jsonrpc":"2.0","method":"notifications/message","params":{}}""")
                                send("""{"jsonrpc":"2.0","id":$id,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}],"structuredContent":{"ok":true,"from":"structured"}}}""")
                            }
                            "textonly" ->
                                send("""{"jsonrpc":"2.0","id":$id,"result":{"content":[{"type":"text","text":"{\"ok\":true,\"from\":\"text\"}"}]}}""")
                            "toolerror" ->
                                send("""{"jsonrpc":"2.0","id":$id,"result":{"isError":true,"content":[{"type":"text","text":"tool exploded"}]}}""")
                            "rpcerror" ->
                                send("""{"jsonrpc":"2.0","id":$id,"error":{"code":-32000,"message":"rpc exploded"}}""")
                            "hang" -> toClient.close()
                        }
                }
            }
        }.apply {
            isDaemon = true
            start()
        }
    }

    override fun close() {
        toServer.close()
        toClient.close()
    }
}

private fun startClient(): McpClient {
    val client = McpClient(FakeTransport())
    client.initialize()
    return client
}

class McpClientTest {
    data class Row(val name: String, val tool: String, val wantFrom: String? = null, val wantErr: String? = null)

    private val rows =
        listOf(
            Row("prefers structuredContent and skips notifications", "structured", wantFrom = "structured"),
            Row("falls back to parsing text content", "textonly", wantFrom = "text"),
            Row("surfaces isError results as errors", "toolerror", wantErr = "tool exploded"),
            Row("surfaces JSON-RPC errors as errors", "rpcerror", wantErr = "rpc exploded"),
        )

    @Test
    fun callTool() {
        for (row in rows) {
            val client = startClient()
            try {
                if (row.wantErr != null) {
                    val err = assertFailsWith<McpException>(row.name) { client.callTool(row.tool, JsonObject()) }
                    assertTrue(err.message!!.contains(row.wantErr), "${row.name}: ${err.message}")
                } else {
                    val result = client.callTool(row.tool, JsonObject())
                    assertEquals(row.wantFrom, result.get("from").asString, row.name)
                }
            } finally {
                client.dispose()
            }
        }
    }

    @Test
    fun serverClosingTheStreamFailsTheCall() {
        val client = startClient()
        try {
            assertFailsWith<McpException> { client.callTool("hang", JsonObject()) }
        } finally {
            client.dispose()
        }
    }
}
