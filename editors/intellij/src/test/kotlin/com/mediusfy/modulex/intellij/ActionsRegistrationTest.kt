package com.mediusfy.modulex.intellij

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
}
