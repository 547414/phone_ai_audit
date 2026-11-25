package com.phoneaiaudit.app.data

import android.content.Context
import android.content.SharedPreferences
import com.phoneaiaudit.app.BuildConfig

class ConfigStore(context: Context) {
    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences("ai_audit_prefs", Context.MODE_PRIVATE)

    var serverUrl: String
        get() = prefs.getString("server_url", BuildConfig.DEFAULT_SERVER_URL)!!.trimEnd('/')
        set(value) = prefs.edit().putString("server_url", value.trim().trimEnd('/')).apply()

    var appToken: String
        get() = prefs.getString("app_token", "")!!.trim()
        set(value) = prefs.edit().putString("app_token", value.trim()).apply()

    var biometricForTerminal: Boolean
        get() = prefs.getBoolean("biometric_terminal", true)
        set(value) = prefs.edit().putBoolean("biometric_terminal", value).apply()

    var biometricForHighRisk: Boolean
        get() = prefs.getBoolean("biometric_high_risk", true)
        set(value) = prefs.edit().putBoolean("biometric_high_risk", value).apply()

    var biometricForFiles: Boolean
        get() = prefs.getBoolean("biometric_files", false)
        set(value) = prefs.edit().putBoolean("biometric_files", value).apply()

    var terminalFontSize: Int
        get() = prefs.getInt("terminal_font_size", 12)
        set(value) = prefs.edit().putInt("terminal_font_size", value.coerceIn(9, 22)).apply()

    // "system", "light", "dark"
    var themeMode: String
        get() = prefs.getString("theme_mode", "system") ?: "system"
        set(value) = prefs.edit().putString("theme_mode", value).apply()

    // Persisted floating gamepad relative anchor (0f..1f)
    var gamepadFracX: Float
        get() = prefs.getFloat("gamepad_frac_x", 0.90f).coerceIn(0f, 1f)
        set(value) = prefs.edit().putFloat("gamepad_frac_x", value.coerceIn(0f, 1f)).apply()

    var gamepadFracY: Float
        get() = prefs.getFloat("gamepad_frac_y", 0.84f).coerceIn(0f, 1f)
        set(value) = prefs.edit().putFloat("gamepad_frac_y", value.coerceIn(0f, 1f)).apply()

    fun isConfigured(): Boolean = serverUrl.startsWith("http") && appToken.isNotBlank()

    fun wsBaseUrl(): String = when {
        serverUrl.startsWith("https://") -> "wss://" + serverUrl.removePrefix("https://")
        serverUrl.startsWith("http://") -> "ws://" + serverUrl.removePrefix("http://")
        else -> "wss://$serverUrl"
    }
}
