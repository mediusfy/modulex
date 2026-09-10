package com.mediusfy.modulex.intellij

import com.intellij.ide.util.PropertiesComponent
import com.intellij.notification.NotificationGroupManager
import com.intellij.notification.NotificationType
import com.intellij.openapi.Disposable
import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.components.Service
import com.intellij.openapi.diagnostic.Logger
import com.intellij.openapi.fileEditor.FileEditorManager
import com.intellij.openapi.fileTypes.FileTypeManager
import com.intellij.openapi.progress.ProgressIndicator
import com.intellij.openapi.progress.ProgressManager
import com.intellij.openapi.progress.Task
import com.intellij.openapi.project.Project
import com.intellij.testFramework.LightVirtualFile
import com.mediusfy.modulex.intellij.core.McpClient
import com.mediusfy.modulex.intellij.core.NO_SERVER_GUIDANCE
import com.mediusfy.modulex.intellij.core.VerificationResult
import com.mediusfy.modulex.intellij.core.VerificationStatus
import com.mediusfy.modulex.intellij.core.resolveServerSpec
import com.mediusfy.modulex.intellij.core.splitCommand
import java.io.File

/**
 * Project-level owner of the MCP client and the plugin's small settings.
 * Every command runs against the local checkout over stdio — no hosted
 * backend, and fully offline while allow-network stays off (the default;
 * networked checks are then reported as skipped, never run).
 */
@Service(Service.Level.PROJECT)
class ModulexService(private val project: Project) : Disposable {
    private val log = Logger.getInstance(ModulexService::class.java)
    private var client: McpClient? = null

    /**
     * Feeds create_handoff's verification field, so a handoff made after a
     * review/verification run records what actually ran.
     */
    @Volatile
    var lastResults: List<VerificationResult> = emptyList()

    val root: File?
        get() = project.basePath?.let(::File)

    // Settings, kept deliberately small (PropertiesComponent per project).
    var serverCommand: String
        get() = PropertiesComponent.getInstance(project).getValue("modulex.server.command", "")
        set(value) = PropertiesComponent.getInstance(project).setValue("modulex.server.command", value)

    var baseRef: String
        get() = PropertiesComponent.getInstance(project).getValue("modulex.review.baseRef", "origin/main")
        set(value) = PropertiesComponent.getInstance(project).setValue("modulex.review.baseRef", value, "origin/main")

    var allowNetwork: Boolean
        get() = PropertiesComponent.getInstance(project).getBoolean("modulex.allowNetwork", false)
        set(value) = PropertiesComponent.getInstance(project).setValue("modulex.allowNetwork", value)

    @Synchronized
    fun client(): McpClient {
        client?.let {
            if (it.alive) {
                return it
            }
            // The server process died (e.g. compile error in the checkout):
            // drop the dead client and respawn instead of failing every
            // action until a manual restart.
            it.dispose()
            client = null
        }
        val projectRoot = root ?: throw IllegalStateException("Modulex needs an open project directory.")
        val configured = splitCommand(serverCommand)
        val spec =
            resolveServerSpec(projectRoot, configured)
                ?: throw IllegalStateException(NO_SERVER_GUIDANCE)
        log.info("starting MCP server: ${spec.command.joinToString(" ")} (cwd ${spec.workingDirectory})")
        val started = McpClient.start(spec) { line -> log.info("[modulex server] $line") }
        client = started
        return started
    }

    @Synchronized
    fun restart() {
        client?.dispose()
        client = null
    }

    /** Runs [work] on a background task; errors surface as notifications. */
    fun runBackground(title: String, work: (ProgressIndicator) -> Unit) {
        ProgressManager.getInstance().run(
            object : Task.Backgroundable(project, title, false) {
                override fun run(indicator: ProgressIndicator) {
                    try {
                        work(indicator)
                    } catch (e: Exception) {
                        log.warn("modulex action failed", e)
                        notify("Modulex: ${e.message}", NotificationType.ERROR)
                    }
                }
            },
        )
    }

    fun notify(content: String, type: NotificationType) {
        NotificationGroupManager.getInstance()
            .getNotificationGroup("Modulex")
            .createNotification(content, type)
            .notify(project)
    }

    fun notifyResults(summary: String, results: List<VerificationResult>) {
        lastResults = results
        val type =
            if (results.any { it.status == VerificationStatus.FAIL }) {
                NotificationType.WARNING
            } else {
                NotificationType.INFORMATION
            }
        notify(summary, type)
    }

    /** Opens read-only content in an editor tab, on the EDT. */
    fun openDocument(fileName: String, content: String) {
        ApplicationManager.getApplication().invokeLater {
            val fileType = FileTypeManager.getInstance().getFileTypeByFileName(fileName)
            val file = LightVirtualFile(fileName, fileType, content)
            file.isWritable = false
            FileEditorManager.getInstance(project).openFile(file, true)
        }
    }

    /** git status --porcelain, mapped to changed paths (rename → new path). */
    fun changedFiles(projectRoot: File): List<String> {
        val process =
            ProcessBuilder("git", "status", "--porcelain")
                .directory(projectRoot)
                .start()
        val out = process.inputStream.bufferedReader().readText()
        if (process.waitFor() != 0) {
            throw IllegalStateException("git status failed: ${process.errorStream.bufferedReader().readText()}")
        }
        return out.lineSequence()
            .filter { it.isNotBlank() }
            .map { it.drop(3).trim() }
            .map { path -> path.substringAfter(" -> ", path) }
            .toList()
    }

    override fun dispose() {
        restart()
    }
}
