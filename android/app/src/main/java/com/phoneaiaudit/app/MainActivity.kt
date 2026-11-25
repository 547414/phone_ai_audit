package com.phoneaiaudit.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.core.content.ContextCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsControllerCompat
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.service.AuditForegroundService
import com.phoneaiaudit.app.ui.ControlScreen
import com.phoneaiaudit.app.ui.FilesScreen
import com.phoneaiaudit.app.ui.IosBottomBar
import com.phoneaiaudit.app.ui.IosTabItem
import com.phoneaiaudit.app.ui.MessagesScreen
import com.phoneaiaudit.app.ui.ProvideIosPalette
import com.phoneaiaudit.app.ui.TerminalScreen
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import org.json.JSONObject

class MainActivity : AppCompatActivity() {

    private var onQrMessage: ((String) -> Unit)? = null

    private val notifPermissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { }

    private val qrLauncher = registerForActivityResult(ScanContract()) { result ->
        val contents = result.contents ?: return@registerForActivityResult
        runCatching {
            val obj = JSONObject(contents)
            val url = obj.optString("server_url")
            val tok = obj.optString("app_token")
            if (url.isBlank() || tok.isBlank()) error("二维码格式无效")
            AuditRepository.config.serverUrl = url
            AuditRepository.config.appToken = tok
            AuditRepository.notifyConfigChanged()
            AuditForegroundService.start(this, reconnect = true)
            onQrMessage?.invoke("扫码配对成功，正在连接 $url")
        }.onFailure {
            onQrMessage?.invoke("二维码解析失败: ${it.message}")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        AuditRepository.init(this)
        requestNotificationPermissionIfNeeded()
        handleIntent(intent)

        if (AuditRepository.config.isConfigured()) {
            AuditForegroundService.start(this)
        } else {
            AuditRepository.selectTab(3)
        }

        setContent {
            val themeMode by AuditRepository.themeMode.collectAsState()
            val systemDark = isSystemInDarkTheme()
            val isDark = when (themeMode) {
                "light" -> false
                "dark" -> true
                else -> systemDark
            }

            SideEffect {
                val controller = WindowInsetsControllerCompat(window, window.decorView)
                controller.isAppearanceLightStatusBars = !isDark
                controller.isAppearanceLightNavigationBars = !isDark
            }

            val m3Scheme = if (isDark) {
                darkColorScheme(
                    primary = Color(0xFF0A84FF),
                    background = Color(0xFF000000),
                    surface = Color(0xFF1C1C1E),
                    surfaceVariant = Color(0xFF2C2C2E)
                )
            } else {
                lightColorScheme(
                    primary = Color(0xFF007AFF),
                    background = Color(0xFFF2F2F7),
                    surface = Color(0xFFFFFFFF),
                    surfaceVariant = Color(0xFFE5E5EA)
                )
            }

            MaterialTheme(colorScheme = m3Scheme) {
                ProvideIosPalette(isDark = isDark) {
                    MainScaffold()
                }
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIntent(intent)
    }

    private fun handleIntent(intent: Intent?) {
        intent ?: return
        val msgId = intent.getStringExtra("message_id")
        if (!msgId.isNullOrBlank()) {
            CoroutineScope(Dispatchers.IO).launch {
                AuditRepository.markMessagesRead(listOf(msgId))
            }
        }
        val termTmux = intent.getStringExtra("open_terminal_tmux")
        if (!termTmux.isNullOrBlank()) {
            AuditRepository.requestOpenTerminal("tmux_attach", termTmux)
            return
        }
        val tab = intent.getIntExtra("open_tab", -1)
        if (tab in 0..3) {
            AuditRepository.selectTab(tab)
        }
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED
            ) {
                notifPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        }
    }

    private fun startQrScan() {
        val opts = ScanOptions().apply {
            setPrompt("扫描 Mac 端 ai-audit pair 二维码")
            setBeepEnabled(false)
            setOrientationLocked(true)
            setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        }
        qrLauncher.launch(opts)
    }

    @Composable
    private fun MainScaffold() {
        val selectedTab by AuditRepository.selectedTab.collectAsState()
        val pending by AuditRepository.pendingApprovals.collectAsState()
        val messages by AuditRepository.messages.collectAsState()
        val isTerminalActive by AuditRepository.isTerminalActive.collectAsState()
        val isFileOverlayActive by AuditRepository.isFileOverlayActive.collectAsState()
        val unreadCount = remember(messages) { messages.count { it.readAt == 0L } }
        val inboxBadge = pending.size + unreadCount
        val snackbarHostState = remember { SnackbarHostState() }
        val scope = rememberCoroutineScope()

        onQrMessage = { msg -> scope.launch { snackbarHostState.showSnackbar(msg) } }
        val showSnack: (String) -> Unit = { msg -> scope.launch { snackbarHostState.showSnackbar(msg) } }

        val tabs = remember(inboxBadge) {
            listOf(
                IosTabItem("消息", Icons.Default.Notifications, inboxBadge),
                IosTabItem("终端", Icons.Default.Terminal),
                IosTabItem("文件", Icons.Default.Folder),
                IosTabItem("控制", Icons.Default.Settings)
            )
        }

        Scaffold(
            contentWindowInsets = WindowInsets(0, 0, 0, 0),
            snackbarHost = { SnackbarHost(snackbarHostState) },
            bottomBar = {
                if (!isTerminalActive && !isFileOverlayActive) {
                    IosBottomBar(
                        items = tabs,
                        selectedIndex = selectedTab.coerceIn(0, 3),
                        onSelect = { AuditRepository.selectTab(it) }
                    )
                }
            }
        ) { innerPadding ->
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .padding(bottom = innerPadding.calculateBottomPadding())
            ) {
                when (selectedTab) {
                    0 -> MessagesScreen(activity = this@MainActivity, onShowSnack = showSnack)
                    1 -> TerminalScreen(activity = this@MainActivity, onShowSnack = showSnack)
                    2 -> FilesScreen(activity = this@MainActivity, onShowSnack = showSnack)
                    else -> ControlScreen(onScanQr = ::startQrScan, onShowSnack = showSnack)
                }
            }
        }
    }
}
