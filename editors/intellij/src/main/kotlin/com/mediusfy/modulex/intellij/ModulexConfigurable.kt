package com.mediusfy.modulex.intellij

import com.intellij.openapi.options.Configurable
import com.intellij.openapi.project.Project
import com.intellij.ui.components.JBCheckBox
import com.intellij.ui.components.JBLabel
import com.intellij.ui.components.JBTextField
import com.intellij.util.ui.FormBuilder
import javax.swing.JComponent

/**
 * Settings → Tools → Modulex. Without this, the server-command and
 * allow-network settings had no writer at all: the error guidance told
 * users to configure a command they had no way to set.
 */
class ModulexConfigurable(private val project: Project) : Configurable {
    private var command = JBTextField()
    private var baseRef = JBTextField()
    private var allowNetwork = JBCheckBox("Allow networked checks (otherwise reported as skipped)")

    private val service: ModulexService
        get() = project.getService(ModulexService::class.java)

    override fun getDisplayName(): String = "Modulex"

    override fun createComponent(): JComponent {
        command.toolTipText =
            "Command starting the modulex MCP server over stdio, run from the project root. " +
                "Quote arguments containing spaces. Empty auto-detects a modulex checkout."
        return FormBuilder.createFormBuilder()
            .addLabeledComponent(JBLabel("Server command:"), command, 1, false)
            .addLabeledComponent(JBLabel("Default review base ref:"), baseRef, 1, false)
            .addComponent(allowNetwork, 1)
            .addComponentFillVertically(javax.swing.JPanel(), 0)
            .panel
    }

    override fun isModified(): Boolean =
        command.text != service.serverCommand ||
            baseRef.text != service.baseRef ||
            allowNetwork.isSelected != service.allowNetwork

    override fun apply() {
        service.serverCommand = command.text
        service.baseRef = baseRef.text.ifBlank { "origin/main" }
        service.allowNetwork = allowNetwork.isSelected
        // A changed command must take effect on the next action.
        service.restart()
    }

    override fun reset() {
        command.text = service.serverCommand
        baseRef.text = service.baseRef
        allowNetwork.isSelected = service.allowNetwork
    }
}
