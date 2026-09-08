package com.mediusfy.modulex.intellij.core

import com.google.gson.JsonArray
import com.google.gson.JsonObject
import java.io.File
import kotlin.test.Test
import kotlin.test.assertTrue
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable

/**
 * Drives the real tools/mcpserver (spawned via go run) with the plugin's
 * MCP client, mirroring the validation done for editors/vscode. Opt-in
 * (MODULEX_E2E=1 and a modulex checkout two levels up) because it builds
 * and runs the Go server.
 */
@EnabledIfEnvironmentVariable(named = "MODULEX_E2E", matches = "1")
class RealServerE2ETest {
    @Test
    fun realServerRoundTrip() {
        val root = File("../..").canonicalFile
        assertTrue(root.resolve("tools/mcpserver").isDirectory, "not a modulex checkout: $root")
        val spec = resolveServerSpec(root, emptyList())!!
        val client = McpClient.start(spec) { }
        try {
            val discover = JsonObject()
            discover.addProperty("root", root.path)
            val repo = client.callTool(Tool.DISCOVER_REPOSITORY, discover)
            assertTrue(repo.has("repository"), "discover_repository returned no repository")

            val recommend = JsonObject()
            recommend.add("changed_files", JsonArray())
            val plan =
                com.google.gson.Gson()
                    .fromJson(
                        client.callTool(Tool.RECOMMEND_VERIFICATION, recommend).getAsJsonObject("plan"),
                        Plan::class.java,
                    )
            assertTrue(plan.fullGates.isNotEmpty(), "expected full gates in the plan")

            val review = JsonObject()
            review.addProperty("root", root.path)
            review.addProperty("base_ref", "HEAD")
            review.addProperty("head_ref", "HEAD")
            val results =
                client.callTool(Tool.REVIEW_DIFF, review).getAsJsonArray("results")
            assertTrue(results.size() > 0, "expected review results")
            // check-changelog and friends judge the branch against
            // origin/main regardless of base_ref, so their verdict depends
            // on the checkout's state; assert the protocol round-trip
            // (well-formed results), not a particular verdict.
            val knownStatuses = setOf("pass", "fail", "skipped", "unavailable", "approval_required")
            for (r in results) {
                val obj = r.asJsonObject
                assertTrue(obj.get("name").asString.isNotEmpty(), "result missing name")
                assertTrue(
                    obj.get("status").asString in knownStatuses,
                    "check ${obj.get("name").asString} has unknown status ${obj.get("status").asString}",
                )
            }
        } finally {
            client.dispose()
        }
    }
}
