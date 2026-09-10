import org.jetbrains.intellij.platform.gradle.TestFrameworkType
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
        // Platform test fixtures for in-IDE tests (ActionsRegistrationTest).
        testFramework(TestFrameworkType.Platform)
    }
    // Gson stays an explicit dependency (not borrowed from the platform)
    // so the core — McpClient, ToolSurface, Render — compiles and tests
    // without the IntelliJ Platform on the classpath.
    implementation("com.google.code.gson:gson:2.11.0")
    testImplementation(kotlin("stdlib"))
    testImplementation(kotlin("test"))
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.4")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    // JUnit 4 must be a compile dependency: BasePlatformTestCase extends
    // JUnit 3's TestCase (shipped in the junit4 jar), and the platform
    // test bootstrap needs it at runtime too. The JUnit3-style fixture
    // tests run via the vintage engine alongside the JUnit 5 core tests.
    testImplementation("junit:junit:4.13.2")
    testRuntimeOnly("org.junit.vintage:junit-vintage-engine:5.11.4")
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
