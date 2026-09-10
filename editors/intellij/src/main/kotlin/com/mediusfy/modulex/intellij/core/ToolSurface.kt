// Typed mirror of editors/tool-surface.json — the shared, editor-agnostic
// description of the read-only modulex MCP server's tool surface (ADR-0035
// plan step 5), bundled verbatim into this plugin's resources at
// modulex/tool-surface.json. The authoritative schemas are
// tools/mcpserver/*.go; update both this file and editors/tool-surface.json
// when they change.
package com.mediusfy.modulex.intellij.core

import com.google.gson.JsonObject
import com.google.gson.annotations.SerializedName

object Tool {
    const val DISCOVER_REPOSITORY = "discover_repository"
    const val READ_CONTRACT = "read_contract"
    const val RECOMMEND_VERIFICATION = "recommend_verification"
    const val RUN_VERIFICATION = "run_verification"
    const val REVIEW_DIFF = "review_diff"
    const val CREATE_HANDOFF = "create_handoff"
}

// provenance.VerificationResult (snake_case json tags).
data class VerificationResult(
    val name: String,
    val category: String,
    val status: String,
    val message: String? = null,
    val reason: String? = null,
)

object VerificationStatus {
    const val PASS = "pass"
    const val FAIL = "fail"
}

// verify.CheckSpec has no json tags, so recommend_verification's plan uses
// Go-default PascalCase field names.
data class PlanCheckSpec(
    @SerializedName("Name") val name: String,
    @SerializedName("Command") val command: String,
    @SerializedName("Category") val category: String,
    @SerializedName("Reason") val reason: String? = null,
    @SerializedName("RequiredTool") val requiredTool: String? = null,
    @SerializedName("Networked") val networked: Boolean = false,
)

data class Plan(
    @SerializedName("FocusedChecks") val focusedChecks: List<PlanCheckSpec> = emptyList(),
    @SerializedName("FullGates") val fullGates: List<PlanCheckSpec> = emptyList(),
)

// Maps a PascalCase plan check (verify.CheckSpec) to the snake_case
// CheckSpecIn shape run_verification expects, omitting unset optionals.
fun planCheckToCheckSpecIn(check: PlanCheckSpec): JsonObject {
    val spec = JsonObject()
    spec.addProperty("name", check.name)
    spec.addProperty("command", check.command)
    spec.addProperty("category", check.category)
    check.reason?.takeIf { it.isNotEmpty() }?.let { spec.addProperty("reason", it) }
    check.requiredTool?.takeIf { it.isNotEmpty() }?.let { spec.addProperty("required_tool", it) }
    if (check.networked) {
        spec.addProperty("networked", true)
    }
    return spec
}
