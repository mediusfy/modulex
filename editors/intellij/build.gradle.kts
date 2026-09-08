import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    kotlin("jvm") version "2.2.20"
    id("org.jetbrains.intellij.platform") version "2.9.0"
}

group = "com.mediusfy.modulex"
version = "0.1.0"

repositories {
    mavenCentral()
    intellijPlatform {
        defaultRepositories()
    }
}

dependencies {
    intellijPlatform {
        intellijIdeaCommunity("2025.1.4.1")
    }
    // Gson stays an explicit dependency (not borrowed from the platform)
    // so the core — McpClient, ToolSurface, Render — compiles and tests
    // without the IntelliJ Platform on the classpath.
    implementation("com.google.code.gson:gson:2.11.0")
    testImplementation(kotlin("stdlib"))
    testImplementation(kotlin("test"))
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.4")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    // The IntelliJ Platform Gradle Plugin's test bootstrap needs JUnit 4 on
    // the classpath even though these tests are JUnit 5.
    testRuntimeOnly("junit:junit:4.13.2")
}

kotlin {
    jvmToolchain(21)
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_21)
    }
}

intellijPlatform {
    pluginConfiguration {
        id = "com.mediusfy.modulex"
        name = "Modulex"
        version = project.version.toString()
        ideaVersion {
            sinceBuild = "251"
        }
    }
}

// The tool surface the plugin drives is described once, editor-agnostically,
// in editors/tool-surface.json (shared with editors/vscode) — bundle that
// exact file instead of keeping a copy.
tasks.processResources {
    from(layout.projectDirectory.file("../tool-surface.json")) {
        into("modulex")
    }
}

tasks.test {
    useJUnitPlatform()
}
