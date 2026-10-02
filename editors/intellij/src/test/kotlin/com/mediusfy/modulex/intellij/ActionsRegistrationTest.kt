package com.mediusfy.modulex.intellij

import com.intellij.ide.trustedProjects.TrustedProjects
import com.intellij.openapi.actionSystem.ActionManager
import com.intellij.testFramework.fixtures.BasePlatformTestCase

/**
 * In-IDE test (real platform fixture): the plugin descriptor loads, every
 * action in the Tools menu group is registered, and the project service
 * instantiates. Complements the platform-free core tests — this is the
 * layer that would catch a broken plugin.xml or glue-code wiring.
 */
class ActionsRegistrationTest : BasePlatformTestCase() {
    fun testEveryActionIsRegistered() {
        val actionManager = ActionManager.getInstance()
        val ids =
            listOf(
                "Modulex.Menu",
                "Modulex.ReviewDiff",
                "Modulex.RunFocusedVerification",
                "Modulex.RunFullGates",
                "Modulex.DiscoverRepository",
                "Modulex.ReadContract",
                "Modulex.CreateHandoff",
                "Modulex.RestartServer",
            )
        for (id in ids) {
            assertNotNull("action $id not registered — plugin.xml broken?", actionManager.getAction(id))
        }
    }

    fun testProjectServiceInstantiates() {
        val service = project.getService(ModulexService::class.java)
        assertNotNull(service)
        assertEquals("origin/main", service.baseRef)
        assertFalse(service.allowNetwork)
    }

    fun testToolSurfaceResourceIsBundled() {
        val resource = javaClass.classLoader.getResource("modulex/tool-surface.json")
        assertNotNull("shared tool-surface.json not bundled into plugin resources", resource)
    }

    /**
     * An untrusted project must never let [ModulexService.client] spawn the
     * local MCP server process — regardless of whether a server command is
     * auto-detected or configured. See [ModulexService.client]'s trust
     * check for why.
     */
    fun testClientRefusesUntrustedProject() {
        val service = project.getService(ModulexService::class.java)
        TrustedProjects.setProjectTrusted(project, false)
        try {
            service.client()
            fail("client() must refuse to start against an untrusted project")
        } catch (e: IllegalStateException) {
            assertTrue(
                "expected a trust-related message, got: ${e.message}",
                e.message?.contains("trusted") == true,
            )
        }
    }

    /**
     * Once trusted, client() must proceed past the trust gate — it may
     * still fail for an unrelated reason (this lightweight test project has
     * no real tools/mcpserver checkout under its root), but that failure
     * must not be the trust-gate message.
     */
    fun testClientProceedsPastTrustGateOnceTrusted() {
        val service = project.getService(ModulexService::class.java)
        TrustedProjects.setProjectTrusted(project, true)
        try {
            service.client()
        } catch (e: IllegalStateException) {
            assertFalse(
                "must not be blocked by the trust gate once trusted: ${e.message}",
                e.message?.contains("trusted") == true,
            )
        }
    }
}
