plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

fun loadRootConfigEnv(): Map<String, String> {
    val result = mutableMapOf<String, String>()
    val envFile = rootProject.file("../config.env")
    if (!envFile.exists()) return result
    val varPattern = Regex("""\$\{([A-Za-z_][A-Za-z0-9_]*)\}""")
    envFile.readLines(Charsets.UTF_8).forEach { rawLine ->
        val line = rawLine.trim()
        if (line.isEmpty() || line.startsWith("#")) return@forEach
        val cleaned = line.removePrefix("export ").trim()
        val idx = cleaned.indexOf('=')
        if (idx <= 0) return@forEach
        val key = cleaned.substring(0, idx).trim()
        var value = cleaned.substring(idx + 1).trim()
        if (value.length >= 2 && (
                (value.startsWith("\"") && value.endsWith("\"")) ||
                    (value.startsWith("'") && value.endsWith("'"))
                )
        ) {
            value = value.substring(1, value.length - 1)
        }
        value = varPattern.replace(value) { m ->
            val ref = m.groupValues[1]
            result[ref] ?: System.getenv(ref) ?: ""
        }
        result[key] = value
    }
    return result
}

val rootConfigEnv = loadRootConfigEnv()

fun cfgValue(key: String, default: String = ""): String {
    val env = System.getenv()
    if (env.containsKey(key)) {
        val fromEnv = env[key]?.trim().orEmpty()
        return if (fromEnv.isEmpty() && default.isNotEmpty()) default else fromEnv
    }
    val fromFile = rootConfigEnv[key]?.trim()
    if (!fromFile.isNullOrEmpty()) return fromFile
    return default
}

fun escapeBuildConfigString(s: String): String =
    s.replace("\\", "\\\\").replace("\"", "\\\"")

val configuredAppId = cfgValue("ANDROID_APPLICATION_ID", "com.phoneaiaudit.app")
val configuredDefaultServerUrl = cfgValue("ANDROID_DEFAULT_SERVER_URL", "")

android {
    namespace = "com.phoneaiaudit.app"
    compileSdk = 35

    defaultConfig {
        applicationId = configuredAppId
        minSdk = 29
        targetSdk = 35
        versionCode = 23
        versionName = "2.7.5"

        buildConfigField(
            "String",
            "DEFAULT_SERVER_URL",
            "\"${escapeBuildConfigString(configuredDefaultServerUrl)}\""
        )
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("debug")
        }
        debug {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.10.01")
    implementation(composeBom)

    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.fragment:fragment-ktx:1.8.5")

    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")

    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    implementation("androidx.biometric:biometric:1.1.0")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
    implementation("androidx.media3:media3-exoplayer:1.5.1")
    implementation("androidx.media3:media3-ui:1.5.1")
    implementation("androidx.media3:media3-datasource-okhttp:1.5.1")
}
