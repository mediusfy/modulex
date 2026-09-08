plugins {
    // Auto-provisions the JDK 21 toolchain the IntelliJ Platform targets,
    // independent of whatever JDK launches Gradle.
    id("org.gradle.toolchains.foojay-resolver-convention") version "1.0.0"
}

rootProject.name = "modulex-intellij"
