package com.mediusfy.modulex.intellij.core

import com.google.gson.Gson
import kotlin.test.Test
import kotlin.test.assertEquals

class ToolSurfaceTest {
    @Test
    fun planParsesGoDefaultPascalCaseFieldNames() {
        val json =
            """
            {
              "FocusedChecks": [
                {"Name": "lint", "Command": "make lint", "Category": "full",
                 "Reason": "required", "RequiredTool": "golangci-lint", "Networked": true}
              ],
              "FullGates": [
                {"Name": "build", "Command": "make build", "Category": "full"}
              ]
            }
            """.trimIndent()
        val plan = Gson().fromJson(json, Plan::class.java)
        assertEquals(1, plan.focusedChecks.size)
        assertEquals("lint", plan.focusedChecks[0].name)
        assertEquals("golangci-lint", plan.focusedChecks[0].requiredTool)
        assertEquals(true, plan.focusedChecks[0].networked)
        assertEquals("build", plan.fullGates[0].name)
        assertEquals(false, plan.fullGates[0].networked)
    }

    data class Row(val name: String, val check: PlanCheckSpec, val want: String)

    private val rows =
        listOf(
            Row(
                "full mapping",
                PlanCheckSpec("lint", "make lint", "full", "required", "golangci-lint", true),
                """{"name":"lint","command":"make lint","category":"full","reason":"required","required_tool":"golangci-lint","networked":true}""",
            ),
            Row(
                "optional fields omitted, not sent as empty",
                PlanCheckSpec("build", "make build", "full"),
                """{"name":"build","command":"make build","category":"full"}""",
            ),
        )

    @Test
    fun planCheckToCheckSpecInMapsToSnakeCase() {
        for (row in rows) {
            assertEquals(row.want, planCheckToCheckSpecIn(row.check).toString(), row.name)
        }
    }
}
