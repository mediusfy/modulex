package com.mediusfy.modulex.intellij.core

import kotlin.test.Test
import kotlin.test.assertEquals

class RenderTest {
    @Test
    fun statusIconCoversEveryStatus() {
        val rows =
            listOf(
                "pass" to "✅",
                "fail" to "❌",
                "skipped" to "⏭️",
                "unavailable" to "⚠️",
                "approval_required" to "🔒",
                "unknown-future-status" to "•",
            )
        for ((status, want) in rows) {
            assertEquals(want, statusIcon(status), status)
        }
    }

    data class Row(val name: String, val results: List<VerificationResult>, val want: String)

    private val rows =
        listOf(
            Row(
                "all pass",
                listOf(
                    VerificationResult("build", "full", "pass"),
                    VerificationResult("lint", "full", "skipped"),
                ),
                "gates: PASS — 1 passed, 0 failed, 1 skipped/unavailable/approval-required",
            ),
            Row(
                "any fail flips the verdict",
                listOf(
                    VerificationResult("build", "full", "pass"),
                    VerificationResult("test", "full", "fail"),
                ),
                "gates: FAIL — 1 passed, 1 failed, 0 skipped/unavailable/approval-required",
            ),
        )

    @Test
    fun summaryLineBucketsStatusesAndPicksTheVerdict() {
        for (row in rows) {
            assertEquals(row.want, summaryLine("gates", row.results), row.name)
        }
    }
}
