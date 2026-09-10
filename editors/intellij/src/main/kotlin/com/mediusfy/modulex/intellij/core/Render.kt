// Renders verification results and plans as plain text for notifications
// and result documents. Kept free of IntelliJ Platform imports so it is
// unit-testable without the platform.
package com.mediusfy.modulex.intellij.core

fun statusIcon(status: String): String =
    when (status) {
        "pass" -> "✅"
        "fail" -> "❌"
        "skipped" -> "⏭️"
        "unavailable" -> "⚠️"
        "approval_required" -> "🔒"
        else -> "•"
    }

fun summaryLine(title: String, results: List<VerificationResult>): String {
    val pass = results.count { it.status == VerificationStatus.PASS }
    val fail = results.count { it.status == VerificationStatus.FAIL }
    val other = results.size - pass - fail
    val verdict = if (fail > 0) "FAIL" else "PASS"
    return "$title: $verdict — $pass passed, $fail failed, $other skipped/unavailable/approval-required"
}

fun resultsText(title: String, results: List<VerificationResult>): String {
    val lines =
        results.map { r ->
            val detail = (r.message ?: r.reason ?: "").trim()
            val suffix = if (detail.isEmpty()) "" else "\n    ${detail.replace("\n", "\n    ")}"
            "${statusIcon(r.status)} ${r.status.padEnd(17)} ${r.name} (${r.category})$suffix"
        }
    return "${summaryLine(title, results)}\n\n${lines.joinToString("\n")}\n"
}

fun planText(title: String, checks: List<PlanCheckSpec>): String {
    val lines =
        checks.map { c ->
            val extras =
                listOfNotNull(
                    c.requiredTool?.takeIf { it.isNotEmpty() }?.let { "requires $it" },
                    if (c.networked) "networked" else null,
                    c.reason?.takeIf { it.isNotEmpty() },
                ).joinToString("; ")
            "- ${c.name} [${c.category}]: ${c.command}" + if (extras.isEmpty()) "" else "  ($extras)"
        }
    return "$title (${checks.size} checks)\n${lines.joinToString("\n")}"
}
