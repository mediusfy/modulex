package com.mediusfy.modulex.intellij

import com.google.gson.Gson
import com.google.gson.GsonBuilder
import com.google.gson.JsonArray
import com.google.gson.JsonObject
import com.google.gson.reflect.TypeToken
import com.intellij.notification.NotificationType
import com.intellij.openapi.actionSystem.ActionUpdateThread
import com.intellij.openapi.actionSystem.AnAction
import com.intellij.openapi.actionSystem.AnActionEvent
import com.intellij.openapi.project.Project
import com.intellij.openapi.ui.Messages
import com.mediusfy.modulex.intellij.core.Plan
import com.mediusfy.modulex.intellij.core.PlanCheckSpec
import com.mediusfy.modulex.intellij.core.Tool
import com.mediusfy.modulex.intellij.core.VerificationResult
import com.mediusfy.modulex.intellij.core.planCheckToCheckSpecIn
import com.mediusfy.modulex.intellij.core.planText
import com.mediusfy.modulex.intellij.core.resultsText
import com.mediusfy.modulex.intellij.core.summaryLine

private val gson = Gson()
private val prettyGson: Gson = GsonBuilder().setPrettyPrinting().create()

private fun parseResults(obj: JsonObject): List<VerificationResult> {
    val type = object : TypeToken<List<VerificationResult>>() {}.type
    return gson.fromJson(obj.getAsJsonArray("results") ?: JsonArray(), type) ?: emptyList()
}

abstract class ModulexAction : AnAction() {
    override fun getActionUpdateThread(): ActionUpdateThread = ActionUpdateThread.BGT

    override fun update(e: AnActionEvent) {
        e.presentation.isEnabledAndVisible = e.project != null
    }

    final override fun actionPerformed(e: AnActionEvent) {
        val project = e.project ?: return
        perform(project, project.getService(ModulexService::class.java))
    }

    abstract fun perform(project: Project, service: ModulexService)
}

class ReviewDiffAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        val baseRef =
            Messages.showInputDialog(
                project,
                "Base ref to review the current checkout against",
                "Modulex: Review Diff",
                null,
                service.baseRef,
                null,
            ) ?: return
        service.baseRef = baseRef
        service.runBackground("Modulex: reviewing diff against $baseRef") {
            val args = JsonObject()
            args.addProperty("root", service.root!!.path)
            args.addProperty("base_ref", baseRef)
            args.addProperty("head_ref", "HEAD")
            args.addProperty("allow_network", service.allowNetwork)
            val results = parseResults(service.client().callTool(Tool.REVIEW_DIFF, args))
            val title = "Review vs $baseRef"
            service.notifyResults(summaryLine(title, results), results)
            service.openDocument("modulex-review.txt", resultsText(title, results))
        }
    }
}

private fun runChecks(service: ModulexService, title: String, checks: List<PlanCheckSpec>) {
    if (checks.isEmpty()) {
        service.notify("$title: nothing to run.", NotificationType.INFORMATION)
        return
    }
    val args = JsonObject()
    args.addProperty("root", service.root!!.path)
    val checksJson = JsonArray()
    checks.forEach { checksJson.add(planCheckToCheckSpecIn(it)) }
    args.add("checks", checksJson)
    args.addProperty("allow_network", service.allowNetwork)
    val results = parseResults(service.client().callTool(Tool.RUN_VERIFICATION, args))
    service.notifyResults(summaryLine(title, results), results)
    service.openDocument(
        "modulex-verification.txt",
        "${planText(title, checks)}\n\n${resultsText(title, results)}",
    )
}

private fun recommendPlan(service: ModulexService, changedFiles: List<String>): Plan {
    val args = JsonObject()
    val files = JsonArray()
    changedFiles.forEach(files::add)
    args.add("changed_files", files)
    val result = service.client().callTool(Tool.RECOMMEND_VERIFICATION, args)
    return gson.fromJson(result.getAsJsonObject("plan"), Plan::class.java) ?: Plan()
}

class RunFocusedVerificationAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        service.runBackground("Modulex: focused verification") {
            val files = service.changedFiles(service.root!!)
            if (files.isEmpty()) {
                service.notify(
                    "Modulex: working tree is clean — no changed files to focus on.",
                    NotificationType.INFORMATION,
                )
                return@runBackground
            }
            runChecks(service, "Focused verification", recommendPlan(service, files).focusedChecks)
        }
    }
}

class RunFullGatesAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        service.runBackground("Modulex: full gates") {
            runChecks(service, "Full gates", recommendPlan(service, emptyList()).fullGates)
        }
    }
}

class DiscoverRepositoryAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        service.runBackground("Modulex: discovering repository") {
            val args = JsonObject()
            args.addProperty("root", service.root!!.path)
            val result = service.client().callTool(Tool.DISCOVER_REPOSITORY, args)
            service.openDocument("modulex-repository.json", prettyGson.toJson(result.get("repository")))
        }
    }
}

class ReadContractAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        service.runBackground("Modulex: reading contract") {
            val args = JsonObject()
            args.addProperty("root", service.root!!.path)
            val result = service.client().callTool(Tool.READ_CONTRACT, args)
            if (result.get("present")?.asBoolean != true) {
                service.notify(
                    "Modulex: no modulex.agent.yaml in this repository.",
                    NotificationType.INFORMATION,
                )
                return@runBackground
            }
            val errors = result.getAsJsonArray("validation_errors")
            if (errors != null && errors.size() > 0) {
                service.notify(
                    "Modulex: contract has ${errors.size()} validation error(s) — see the opened document.",
                    NotificationType.WARNING,
                )
            }
            service.openDocument("modulex-contract.json", prettyGson.toJson(result))
        }
    }
}

class CreateHandoffAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        val agentName =
            Messages.showInputDialog(
                project,
                "Agent name recorded in the handoff envelope",
                "Modulex: Create Handoff",
                null,
                "intellij",
                null,
            ) ?: return
        service.runBackground("Modulex: creating handoff envelope") {
            val args = JsonObject()
            args.addProperty("root", service.root!!.path)
            args.addProperty("agent_name", agentName)
            args.add("verification", gson.toJsonTree(service.lastResults))
            val result = service.client().callTool(Tool.CREATE_HANDOFF, args)
            service.openDocument("modulex-envelope.json", prettyGson.toJson(result.get("envelope")))
        }
    }
}

class RestartServerAction : ModulexAction() {
    override fun perform(project: Project, service: ModulexService) {
        service.restart()
        service.notify(
            "Modulex: MCP server stopped; it restarts on the next command.",
            NotificationType.INFORMATION,
        )
    }
}
