plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Set by android/build.sh: the version of the daemon inside, the signing key.
fun prop(name: String): String? = providers.gradleProperty(name).orNull

android {
    namespace = "io.github.oldatstec.vkturn"
    compileSdk = 35
    buildToolsVersion = "35.0.0"

    defaultConfig {
        applicationId = "io.github.oldatstec.vkturn"
        minSdk = 26
        targetSdk = 35
        versionCode = (prop("vkturnVersionCode") ?: "1").toInt()
        versionName = prop("vkturnVersionName") ?: "dev"
    }

    signingConfigs {
        create("release") {
            prop("vkturnKeystore")?.let { storeFile = rootProject.file(it) }
            storePassword = prop("vkturnKeystorePassword")
            keyAlias = prop("vkturnKeyAlias")
            keyPassword = prop("vkturnKeyPassword")
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = if (prop("vkturnKeystore") != null) signingConfigs.getByName("release") else null
        }
    }

    // One APK per ABI (the daemon is ~10 MB per architecture) and one for all.
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a", "armeabi-v7a", "x86_64")
            isUniversalApk = true
        }
    }

    // The daemon is an executable, not a library: it must lie unpacked in
    // nativeLibraryDir, the one place an app may run a file from.
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    lint {
        checkReleaseBuilds = false
    }

    dependenciesInfo {
        includeInApk = false
        includeInBundle = false
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}
