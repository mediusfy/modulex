package com.mediusfy.modulex.intellij.core

import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals

class ServerResolverTest {
    @Test
    fun resolutionOrder() {
        val modulexCheckout = Files.createTempDirectory("modulex-intellij-test").toFile()
        modulexCheckout.resolve("tools/mcpserver/cmd/mcpserver").mkdirs()
        val plainRepo = Files.createTempDirectory("modulex-intellij-test").toFile()

        data class Row(
            val name: String,
            val root: java.io.File,
            val configured: List<String>,
            val want: ServerSpec?,
        )
        val rows =
            listOf(
                Row(
                    "explicit setting wins even in a modulex checkout",
                    modulexCheckout,
                    listOf("/usr/local/bin/mcpserver", "--flag"),
                    ServerSpec(listOf("/usr/local/bin/mcpserver", "--flag"), modulexCheckout),
                ),
                Row(
                    "modulex checkout auto-detects go run against the nested module",
                    modulexCheckout,
                    emptyList(),
                    ServerSpec(
                        listOf("go", "run", "-C", "tools/mcpserver", "./cmd/mcpserver"),
                        modulexCheckout,
                    ),
                ),
                Row("plain repository with no setting resolves nothing", plainRepo, emptyList(), null),
            )
        try {
            for (row in rows) {
                assertEquals(row.want, resolveServerSpec(row.root, row.configured), row.name)
            }
        } finally {
            modulexCheckout.deleteRecursively()
            plainRepo.deleteRecursively()
        }
    }
}
