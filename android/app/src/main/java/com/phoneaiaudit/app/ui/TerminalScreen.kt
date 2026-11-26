package com.phoneaiaudit.app.ui

import android.annotation.SuppressLint
import android.content.pm.ActivityInfo
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebView
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.pointer.positionChange
import kotlin.math.hypot
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.DeleteOutline
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Keyboard
import androidx.compose.material.icons.filled.KeyboardHide
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.ScreenRotation
import androidx.compose.material.icons.filled.SportsEsports
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.fragment.app.FragmentActivity
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.TerminalLaunchRequest
import com.phoneaiaudit.app.data.TmuxSession
import com.phoneaiaudit.app.terminal.TerminalSocketManager
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicReference
import kotlin.math.roundToInt

@SuppressLint("SetJavaScriptEnabled")
@Composable
fun TerminalScreen(
    activity: FragmentActivity,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val status by AuditRepository.status.collectAsState()
    val sessions by AuditRepository.tmuxSessions.collectAsState()
    val pendingLaunch by AuditRepository.pendingTerminalLaunch.collectAsState()
    val scope = rememberCoroutineScope()

    var activeRequest by remember { mutableStateOf<TerminalLaunchRequest?>(null) }
    var pendingTicket by remember { mutableStateOf<String?>(null) }
    var connected by remember { mutableStateOf(false) }
    var headerLabel by remember { mutableStateOf("未连接") }
    var fontSize by remember { mutableIntStateOf(AuditRepository.config.terminalFontSize) }
    var isLandscape by remember { mutableStateOf(false) }
    var isWideMode by remember { mutableStateOf(true) }
    var showFullKeyboard by remember { mutableStateOf(false) }
    var composeInput by remember { mutableStateOf("") }
    var termCols by remember { mutableIntStateOf(135) }
    var termRows by remember { mutableIntStateOf(35) }
    val webViewRef = remember { AtomicReference<WebView?>(null) }

    var noteTarget by remember { mutableStateOf<TmuxSession?>(null) }
    var noteInput by remember { mutableStateOf("") }
    var killTargetName by remember { mutableStateOf<String?>(null) }

    val socketManager = remember {
        TerminalSocketManager(
            onOutputBase64 = { b64 ->
                activity.runOnUiThread {
                    webViewRef.get()?.evaluateJavascript("window.writeBase64('$b64');", null)
                }
            },
            onSystemLine = { line ->
                val quoted = JSONObject.quote(line)
                activity.runOnUiThread {
                    webViewRef.get()?.evaluateJavascript("window.writeSystemLine($quoted);", null)
                }
            },
            onRemoteSize = { cols, rows ->
                activity.runOnUiThread {
                    termCols = cols
                    termRows = rows
                    webViewRef.get()?.evaluateJavascript("window.setRemoteSize($cols, $rows);", null)
                }
            },
            onStateChange = { ok, label ->
                activity.runOnUiThread {
                    connected = ok
                    headerLabel = label
                }
            }
        )
    }

    LaunchedEffect(activeRequest) {
        AuditRepository.setTerminalActive(activeRequest != null)
    }

    LaunchedEffect(p.isDark, activeRequest) {
        if (activeRequest != null) {
            webViewRef.get()?.evaluateJavascript("window.setTheme(${p.isDark});", null)
        }
    }

    fun startTerminal(req: TerminalLaunchRequest) {
        if (!status.settings.terminalEnabled) {
            onShowSnack("远程终端已在控制面板中关闭")
            return
        }
        if (!status.macOnline) {
            onShowSnack("Mac 守护进程当前不在线")
            return
        }
        val launch = {
            scope.launch {
                val tkRes = AuditRepository.issueTerminalTicket()
                tkRes.onSuccess { ticket ->
                    pendingTicket = ticket
                    isWideMode = true
                    activeRequest = req
                }.onFailure {
                    onShowSnack("无法启动终端: ${it.message}")
                }
            }
        }
        if (AuditRepository.config.biometricForTerminal) {
            BiometricHelper.authenticate(
                activity = activity,
                title = "解锁 Mac 远程终端",
                subtitle = if (req.target.isNotBlank()) "连接 tmux: ${req.target}" else "新建 Mac 终端并同步弹出窗口",
                onSuccess = { launch() },
                onError = { onShowSnack("已取消终端认证: $it") }
            )
        } else {
            launch()
        }
    }

    fun exitTerminalView() {
        socketManager.disconnect()
        webViewRef.set(null)
        activeRequest = null
        isLandscape = false
        isWideMode = true
        showFullKeyboard = false
        activity.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        scope.launch { AuditRepository.refreshTmuxSessions() }
    }

    LaunchedEffect(Unit) {
        AuditRepository.refreshTmuxSessions()
    }

    LaunchedEffect(pendingLaunch) {
        val req = AuditRepository.consumeTerminalLaunch()
        if (req != null) {
            startTerminal(req)
        }
    }

    DisposableEffect(Unit) {
        onDispose {
            AuditRepository.setTerminalActive(false)
            socketManager.disconnect()
            webViewRef.set(null)
            activity.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        }
    }

    // Shared dialogs (Note edit + Kill confirm)
    noteTarget?.let { target ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { noteTarget = null },
            title = { Text("终端备注 (${target.name})", fontWeight = FontWeight.SemiBold, fontSize = 16.sp) },
            text = {
                OutlinedTextField(
                    value = noteInput,
                    onValueChange = { noteInput = it },
                    placeholder = { Text("例如：修登录Bug / 查天气 / 后端联调", fontSize = 13.sp) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                IosButton(
                    text = "保存",
                    onClick = {
                        val name = target.name
                        val txt = noteInput
                        noteTarget = null
                        scope.launch {
                            AuditRepository.setTerminalNote(name, txt)
                                .onSuccess { onShowSnack(if (txt.isBlank()) "备注已清除" else "备注已保存") }
                                .onFailure { onShowSnack("保存失败: ${it.message}") }
                        }
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { noteTarget = null }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }

    killTargetName?.let { targetName ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { killTargetName = null },
            title = { Text("彻底关闭终端？", fontWeight = FontWeight.SemiBold) },
            text = {
                Text(
                    "将结束 Mac 上的 tmux 会话「$targetName」及其正在运行的进程与窗口。",
                    color = p.secondaryLabel,
                    fontSize = 13.sp
                )
            },
            confirmButton = {
                IosButton(
                    text = "确认关闭",
                    containerColor = p.red,
                    onClick = {
                        val name = targetName
                        killTargetName = null
                        val wasActive = activeRequest?.target == name
                        scope.launch {
                            AuditRepository.killTmuxSession(name)
                                .onSuccess {
                                    onShowSnack("已关闭终端 $name")
                                    if (wasActive) exitTerminalView()
                                }
                                .onFailure { onShowSnack("关闭失败: ${it.message}") }
                        }
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { killTargetName = null }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }

    if (activeRequest == null) {
        Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
            IosPageHeader(
                title = "终端",
                subtitle = if (status.macOnline)
                    "Mac 在线 · ${status.macHostname.ifBlank { "已连接" }}"
                else
                    "Mac 守护当前离线",
                trailing = {
                    IconButton(onClick = {
                        scope.launch {
                            AuditRepository.refreshTmuxSessions()
                                .onSuccess { list -> onShowSnack("刷新成功 · 共 ${list.size} 个终端") }
                                .onFailure { onShowSnack("刷新失败: ${it.message}") }
                        }
                    }) {
                        Icon(Icons.Default.Refresh, contentDescription = "刷新", tint = p.accent)
                    }
                }
            )

            LazyColumn(
                modifier = Modifier.fillMaxSize(),
                contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 20.dp),
                verticalArrangement = Arrangement.spacedBy(14.dp)
            ) {
                item {
                    IosButton(
                        text = "新建 Mac 终端（电脑端同步弹窗）",
                        icon = Icons.Default.Add,
                        enabled = status.macOnline && status.settings.terminalEnabled,
                        onClick = {
                            val newName = "phone-" + java.util.UUID.randomUUID().toString().replace("-", "").take(6)
                            startTerminal(TerminalLaunchRequest("new_shell", newName))
                        },
                        modifier = Modifier.fillMaxWidth()
                    )
                }

                if (sessions.isEmpty()) {
                    item {
                        IosSection(
                            title = "活跃会话",
                            footer = "在 Mac 终端用 ai-tmux claude / ai-tmux codex / ai-tmux agy 启动 AI，就会自动出现在这里。"
                        ) {
                            Column(
                                modifier = Modifier.fillMaxWidth().padding(vertical = 28.dp, horizontal = 20.dp),
                                horizontalAlignment = Alignment.CenterHorizontally
                            ) {
                                Icon(Icons.Default.Terminal, null, tint = p.tertiaryLabel, modifier = Modifier.size(32.dp))
                                Spacer(Modifier.height(8.dp))
                                Text("暂无活跃的 tmux 会话", color = p.label, fontSize = 15.sp, fontWeight = FontWeight.Medium)
                            }
                        }
                    }
                } else {
                    item {
                        IosSection(
                            title = "活跃会话 (${sessions.size})",
                            footer = "点击任意行即可实时交互；右侧可打备注或彻底关闭 Mac 端该终端。"
                        ) {
                            sessions.forEachIndexed { idx, sess ->
                                TmuxSessionRow(
                                    session = sess,
                                    enabled = status.macOnline && status.settings.terminalEnabled,
                                    onAttach = { startTerminal(TerminalLaunchRequest("tmux_attach", sess.name)) },
                                    onEditNote = {
                                        noteInput = sess.note
                                        noteTarget = sess
                                    },
                                    onKill = { killTargetName = sess.name }
                                )
                                if (idx < sessions.lastIndex) {
                                    IosDivider(startIndent = 16.dp)
                                }
                            }
                        }
                    }
                }
            }
        }
        return
    }

    // Active full-screen interactive terminal view
    val focusManager = LocalFocusManager.current
    val keyboardController = LocalSoftwareKeyboardController.current

    BackHandler {
        if (showFullKeyboard) {
            showFullKeyboard = false
        } else {
            exitTerminalView()
        }
    }

    val imeBottom = WindowInsets.ime.getBottom(LocalDensity.current)
    LaunchedEffect(imeBottom > 0) {
        if (imeBottom > 0) {
            showFullKeyboard = false
            delay(140)
            webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
            delay(200)
            webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
        }
    }

    LaunchedEffect(showFullKeyboard) {
        if (showFullKeyboard) {
            focusManager.clearFocus()
            keyboardController?.hide()
            delay(120)
            webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
        }
    }

    val activeTargetName = activeRequest?.target.orEmpty()
    val matchedSession = remember(sessions, activeTargetName) {
        sessions.firstOrNull { it.name == activeTargetName }
    }
    val displayTitle = matchedSession?.note?.takeIf { it.isNotBlank() } ?: headerLabel

    val termBg = if (p.isDark) Color(0xFF000000) else Color(0xFFFFFFFF)
    val barBg = p.tabBarBg
    val chipBg = p.secondaryFill

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(termBg)
            .imePadding()
    ) {
        // Compact iOS terminal top bar
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(barBg)
                .statusBarsPadding()
                .padding(horizontal = 6.dp, vertical = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.weight(1f)) {
                IconButton(onClick = { exitTerminalView() }, modifier = Modifier.size(34.dp)) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, "退出查看", tint = p.accent, modifier = Modifier.size(20.dp))
                }
                Box(
                    modifier = Modifier
                        .size(8.dp)
                        .clip(CircleShape)
                        .background(if (connected) p.green else p.orange)
                )
                Spacer(Modifier.width(6.dp))
                Text(
                    displayTitle,
                    color = p.label,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                SmallTermChip(
                    label = if (isWideMode) "原屏" else "适屏",
                    bg = if (isWideMode) p.accent.copy(alpha = 0.22f) else chipBg,
                    fg = if (isWideMode) p.accent else p.label
                ) {
                    isWideMode = !isWideMode
                    webViewRef.get()?.evaluateJavascript("window.setWideMode($isWideMode);", null)
                    onShowSnack(if (isWideMode) "原屏模式：1:1 保持电脑视图，可左右上下拖动" else "适屏模式：按手机宽度折行")
                }
                SmallTermChip("A-", chipBg, p.label) {
                    fontSize = (fontSize - 1).coerceAtLeast(9)
                    AuditRepository.config.terminalFontSize = fontSize
                    webViewRef.get()?.evaluateJavascript("window.setFontSize($fontSize);", null)
                }
                SmallTermChip("A+", chipBg, p.label) {
                    fontSize = (fontSize + 1).coerceAtMost(20)
                    AuditRepository.config.terminalFontSize = fontSize
                    webViewRef.get()?.evaluateJavascript("window.setFontSize($fontSize);", null)
                }
                IconButton(
                    onClick = {
                        isLandscape = !isLandscape
                        activity.requestedOrientation = if (isLandscape) {
                            ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE
                        } else {
                            ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
                        }
                    },
                    modifier = Modifier.size(32.dp)
                ) {
                    Icon(Icons.Default.ScreenRotation, "横竖屏", tint = p.accent, modifier = Modifier.size(17.dp))
                }
                if (activeTargetName.isNotBlank()) {
                    IconButton(
                        onClick = { killTargetName = activeTargetName },
                        modifier = Modifier.size(32.dp)
                    ) {
                        Icon(Icons.Default.PowerSettingsNew, "彻底关闭终端", tint = p.red, modifier = Modifier.size(18.dp))
                    }
                }
            }
        }
        HorizontalDivider(thickness = 0.5.dp, color = p.separator.copy(alpha = 0.7f))

        // Terminal viewport + Draggable Floating Gamepad Overlay
        BoxWithConstraints(modifier = Modifier.weight(1f).fillMaxWidth()) {
            val containerW = constraints.maxWidth.toFloat()
            val containerH = constraints.maxHeight.toFloat()

            AndroidView(
                modifier = Modifier.fillMaxSize(),
                factory = { ctx ->
                    WebView(ctx).apply {
                        layoutParams = ViewGroup.LayoutParams(
                            ViewGroup.LayoutParams.MATCH_PARENT,
                            ViewGroup.LayoutParams.MATCH_PARENT
                        )
                        setBackgroundColor(if (p.isDark) android.graphics.Color.BLACK else android.graphics.Color.WHITE)
                        webChromeClient = WebChromeClient()
                        settings.javaScriptEnabled = true
                        settings.domStorageEnabled = true
                        settings.allowFileAccess = true
                        @Suppress("DEPRECATION")
                        settings.allowFileAccessFromFileURLs = true
                        @Suppress("DEPRECATION")
                        settings.allowUniversalAccessFromFileURLs = true
                        addJavascriptInterface(object {
                            @JavascriptInterface
                            fun onInput(data: String) {
                                socketManager.sendInput(data)
                            }

                            @JavascriptInterface
                            fun onResize(cols: Int, rows: Int) {
                                activity.runOnUiThread {
                                    termCols = cols
                                    termRows = rows
                                }
                                socketManager.resize(cols, rows)
                            }

                            @JavascriptInterface
                            fun onReady(cols: Int, rows: Int) {
                                activity.runOnUiThread {
                                    termCols = cols
                                    termRows = rows
                                    val wv = webViewRef.get()
                                    wv?.evaluateJavascript("window.setTheme(${p.isDark}); window.setFontSize($fontSize);", null)
                                    val req = activeRequest
                                    val tk = pendingTicket
                                    if (req != null && tk != null) {
                                        pendingTicket = null
                                        socketManager.connect(tk, req.mode, req.target, cols, rows)
                                    } else {
                                        socketManager.resize(cols, rows)
                                    }
                                }
                            }
                        }, "AndroidBridge")
                        webViewRef.set(this)
                        loadUrl("file:///android_asset/terminal/index.html")
                    }
                }
            )

            FloatingGamepadOverlay(
                containerWidthPx = containerW,
                containerHeightPx = containerH,
                fullKeyboardOpen = showFullKeyboard,
                onToggleFullKeyboard = { showFullKeyboard = !showFullKeyboard },
                onSendKey = { seq -> socketManager.sendInput(seq) }
            )
        }

        HorizontalDivider(thickness = 0.5.dp, color = p.separator.copy(alpha = 0.7f))

        // Clean Chinese IME prompt bar + Full Terminal Keyboard toggle button
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(barBg)
                .then(if (!showFullKeyboard) Modifier.navigationBarsPadding() else Modifier)
                .padding(horizontal = 8.dp, vertical = 7.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(7.dp)
        ) {
            Surface(
                onClick = { showFullKeyboard = !showFullKeyboard },
                shape = RoundedCornerShape(11.dp),
                color = if (showFullKeyboard) p.accent else chipBg,
                border = BorderStroke(
                    0.8.dp,
                    if (showFullKeyboard) p.accent else p.separator.copy(alpha = 0.7f)
                ),
                modifier = Modifier.size(44.dp)
            ) {
                Box(contentAlignment = Alignment.Center) {
                    Icon(
                        imageVector = if (showFullKeyboard) Icons.Default.KeyboardHide else Icons.Default.Keyboard,
                        contentDescription = if (showFullKeyboard) "收起完整全键盘" else "展开完整全键盘",
                        tint = if (showFullKeyboard) Color.White else p.accent,
                        modifier = Modifier.size(22.dp)
                    )
                }
            }
            OutlinedTextField(
                value = composeInput,
                onValueChange = { composeInput = it },
                placeholder = { Text("输入指令或中文提示词...", fontSize = 13.sp, color = p.secondaryLabel) },
                singleLine = true,
                shape = RoundedCornerShape(12.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = p.accent,
                    unfocusedBorderColor = p.separator,
                    focusedTextColor = p.label,
                    unfocusedTextColor = p.label,
                    focusedContainerColor = p.card,
                    unfocusedContainerColor = p.card
                ),
                modifier = Modifier
                    .weight(1f)
                    .onFocusChanged { state ->
                        if (state.isFocused) {
                            showFullKeyboard = false
                            scope.launch {
                                webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
                                delay(220)
                                webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
                            }
                        }
                    }
            )
            IosButton(
                text = "",
                icon = Icons.AutoMirrored.Filled.Send,
                onClick = {
                    val txt = composeInput
                    composeInput = ""
                    scope.launch {
                        if (txt.isNotEmpty()) {
                            socketManager.sendInput(txt)
                            delay(50)
                        }
                        socketManager.sendInput("\r")
                        delay(80)
                        webViewRef.get()?.evaluateJavascript("window.scrollToCursor();", null)
                    }
                },
                modifier = Modifier.width(50.dp)
            )
        }

        if (showFullKeyboard) {
            HorizontalDivider(thickness = 0.5.dp, color = p.separator.copy(alpha = 0.7f))
            FullTerminalKeyboard(
                compact = isLandscape,
                onSendKey = { seq ->
                    socketManager.sendInput(seq)
                },
                onClose = { showFullKeyboard = false }
            )
        }
    }
}

private data class GamepadShortcut(
    val label: String,
    val seq: String,
    val tone: String = "normal" // "normal", "accent", "red", "orange", "green", "muted"
)

/**
 * Draggable game-pad style floating overlay for directional keys & multi-page terminal/Agent shortcuts.
 * Both collapsed orb and expanded panel are smoothly draggable and persist their last position.
 */
@Composable
private fun FloatingGamepadOverlay(
    containerWidthPx: Float,
    containerHeightPx: Float,
    fullKeyboardOpen: Boolean,
    onToggleFullKeyboard: () -> Unit,
    onSendKey: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val density = LocalDensity.current
    var expanded by remember { mutableStateOf(false) }
    var activePage by remember { mutableIntStateOf(0) } // 0=常用, 1=Agent, 2=导航

    val maxAvailWidthDp = with(density) { (containerWidthPx / density.density - 16f).coerceAtLeast(280f) }
    val expandedWidthDp = maxAvailWidthDp.coerceAtMost(344f).dp

    val panelW = with(density) { (if (expanded) expandedWidthDp else 48.dp).toPx() }
    val panelH = with(density) { (if (expanded) 226.dp else 48.dp).toPx() }
    val padPx = with(density) { 8.dp.toPx() }

    val minX = padPx
    val maxX = (containerWidthPx - panelW - padPx).coerceAtLeast(minX)
    val minY = padPx
    val maxY = (containerHeightPx - panelH - padPx).coerceAtLeast(minY)
    val spanX = (maxX - minX).coerceAtLeast(1f)
    val spanY = (maxY - minY).coerceAtLeast(1f)

    var fracX by remember { mutableFloatStateOf(AuditRepository.config.gamepadFracX) }
    var fracY by remember { mutableFloatStateOf(AuditRepository.config.gamepadFracY) }

    fun applyDrag(dx: Float, dy: Float) {
        val curX = minX + fracX * spanX
        val curY = minY + fracY * spanY
        val nextX = (curX + dx).coerceIn(minX, maxX)
        val nextY = (curY + dy).coerceIn(minY, maxY)
        fracX = ((nextX - minX) / spanX).coerceIn(0f, 1f)
        fracY = ((nextY - minY) / spanY).coerceIn(0f, 1f)
    }

    fun persistPos() {
        AuditRepository.config.gamepadFracX = fracX
        AuditRepository.config.gamepadFracY = fracY
    }

    val posX = minX + fracX * spanX
    val posY = minY + fracY * spanY

    val glassBg = if (p.isDark) Color(0xDC1C1C1E) else Color(0xEEFFFFFF)
    val keyBg = if (p.isDark) Color(0xFF2C2C2E) else Color(0xFFE9E9EE)
    val borderCol = p.separator.copy(alpha = 0.85f)

    val pageTabs = listOf("常用", "Agent", "导航")
    val pageKeys = remember {
        listOf(
            // Page 0: 常用（含 Shift+Tab 与高频中断/确认）
            listOf(
                GamepadShortcut("Esc", "\u001B"),
                GamepadShortcut("Tab", "\t"),
                GamepadShortcut("Shift+Tab", "\u001B[Z"),
                GamepadShortcut("Ctrl+C", "\u0003", "red"),
                GamepadShortcut("Ctrl+D", "\u0004", "orange"),
                GamepadShortcut("y 确认", "y\r", "green"),
                GamepadShortcut("n 取消", "n\r"),
                GamepadShortcut("/", "/")
            ),
            // Page 1: Agent 专属高频快捷键（Claude Code / Codex / Antigravity）
            listOf(
                GamepadShortcut("Shift+Tab", "\u001B[Z"),
                GamepadShortcut("Ctrl+O 展开", "\u000F"),
                GamepadShortcut("Ctrl+G 编辑", "\u0007"),
                GamepadShortcut("Ctrl+T 任务", "\u0014"),
                GamepadShortcut("Ctrl+R 历史", "\u0012"),
                GamepadShortcut("Ctrl+B 后台", "\u0002"),
                GamepadShortcut("Ctrl+U 清行", "\u0015", "orange"),
                GamepadShortcut("Alt+⏎ 换行", "\u001B\r", "green")
            ),
            // Page 2: 导航与数字选单
            listOf(
                GamepadShortcut("1", "1"),
                GamepadShortcut("2", "2"),
                GamepadShortcut("3", "3"),
                GamepadShortcut("⌫ 退格", "\u007F", "orange"),
                GamepadShortcut("PgUp", "\u001B[5~"),
                GamepadShortcut("PgDn", "\u001B[6~"),
                GamepadShortcut("Ctrl+L 清屏", "\u000C"),
                GamepadShortcut("q 退翻页", "q", "muted")
            )
        )
    }

    Box(
        modifier = Modifier.offset { IntOffset(posX.roundToInt(), posY.roundToInt()) }
    ) {
        if (!expanded) {
            // Single unified gesture detector: tap (< slop) expands, drag (>= slop) moves & saves!
            Box(
                modifier = Modifier
                    .size(48.dp)
                    .shadow(6.dp, CircleShape)
                    .clip(CircleShape)
                    .background(glassBg)
                    .pointerInput(containerWidthPx, containerHeightPx) {
                        awaitEachGesture {
                            val down = awaitFirstDown(requireUnconsumed = false)
                            var totalDx = 0f
                            var totalDy = 0f
                            var dragging = false
                            val slop = viewConfiguration.touchSlop
                            while (true) {
                                val event = awaitPointerEvent()
                                val change = event.changes.firstOrNull { it.id == down.id } ?: break
                                if (!change.pressed) {
                                    if (dragging) {
                                        persistPos()
                                    } else {
                                        expanded = true
                                    }
                                    break
                                }
                                val d = change.positionChange()
                                totalDx += d.x
                                totalDy += d.y
                                if (!dragging && hypot(totalDx, totalDy) > slop) {
                                    dragging = true
                                }
                                if (dragging) {
                                    change.consume()
                                    applyDrag(d.x, d.y)
                                }
                            }
                        }
                    },
                contentAlignment = Alignment.Center
            ) {
                Surface(
                    shape = CircleShape,
                    color = Color.Transparent,
                    border = BorderStroke(1.2.dp, p.accent.copy(alpha = 0.75f)),
                    modifier = Modifier.fillMaxSize()
                ) {
                    Box(contentAlignment = Alignment.Center) {
                        Icon(
                            Icons.Default.SportsEsports,
                            contentDescription = "展开或拖动方向键悬浮球",
                            tint = p.accent,
                            modifier = Modifier.size(23.dp)
                        )
                    }
                }
            }
        } else {
            Surface(
                shape = RoundedCornerShape(20.dp),
                color = glassBg,
                border = BorderStroke(0.8.dp, borderCol),
                modifier = Modifier
                    .width(expandedWidthDp)
                    .shadow(10.dp, RoundedCornerShape(20.dp))
                    .pointerInput(containerWidthPx, containerHeightPx) {
                        detectDragGestures(
                            onDragEnd = { persistPos() },
                            onDragCancel = { persistPos() }
                        ) { change, dragAmount ->
                            change.consume()
                            applyDrag(dragAmount.x, dragAmount.y)
                        }
                    }
            ) {
                Column(modifier = Modifier.padding(10.dp), verticalArrangement = Arrangement.spacedBy(9.dp)) {
                    // Drag header + larger shortcut page switcher + full keyboard shortcut + collapse button
                    Row(
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 2.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(6.dp)
                        ) {
                            Icon(Icons.Default.SportsEsports, null, tint = p.accent, modifier = Modifier.size(17.dp))
                            pageTabs.forEachIndexed { idx, tabTitle ->
                                val selected = activePage == idx
                                Surface(
                                    onClick = { activePage = idx },
                                    shape = RoundedCornerShape(9.dp),
                                    color = if (selected) p.accent else keyBg,
                                    modifier = Modifier.height(30.dp)
                                ) {
                                    Box(
                                        contentAlignment = Alignment.Center,
                                        modifier = Modifier.padding(horizontal = 11.dp)
                                    ) {
                                        Text(
                                            text = tabTitle,
                                            color = if (selected) Color.White else p.secondaryLabel,
                                            fontSize = 12.sp,
                                            fontWeight = if (selected) FontWeight.Bold else FontWeight.Medium
                                        )
                                    }
                                }
                            }
                        }
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(7.dp)
                        ) {
                            Box(
                                modifier = Modifier
                                    .size(30.dp)
                                    .clip(CircleShape)
                                    .background(if (fullKeyboardOpen) p.accent else keyBg)
                                    .clickable {
                                        if (!fullKeyboardOpen) expanded = false
                                        onToggleFullKeyboard()
                                    },
                                contentAlignment = Alignment.Center
                            ) {
                                Icon(
                                    Icons.Default.Keyboard,
                                    "完整全键盘",
                                    tint = if (fullKeyboardOpen) Color.White else p.accent,
                                    modifier = Modifier.size(16.dp)
                                )
                            }
                            Box(
                                modifier = Modifier
                                    .size(30.dp)
                                    .clip(CircleShape)
                                    .background(keyBg)
                                    .clickable { expanded = false },
                                contentAlignment = Alignment.Center
                            ) {
                                Icon(Icons.Default.Close, "收起", tint = p.secondaryLabel, modifier = Modifier.size(16.dp))
                            }
                        }
                    }

                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(10.dp),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        // Left: Thumb-friendly D-Pad with center Enter
                        Column(
                            horizontalAlignment = Alignment.CenterHorizontally,
                            verticalArrangement = Arrangement.spacedBy(6.dp)
                        ) {
                            PadKey("↑", keyBg, p.label, 44.dp, 42.dp) { onSendKey("\u001B[A") }
                            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                PadKey("←", keyBg, p.label, 44.dp, 42.dp) { onSendKey("\u001B[D") }
                                PadKey("⏎", p.accent, Color.White, 44.dp, 42.dp, circle = true) { onSendKey("\r") }
                                PadKey("→", keyBg, p.label, 44.dp, 42.dp) { onSendKey("\u001B[C") }
                            }
                            PadKey("↓", keyBg, p.label, 44.dp, 42.dp) { onSendKey("\u001B[B") }
                        }

                        // Right: 2-column x 4-row thumb-friendly shortcut grid for selected page
                        val currentKeys = pageKeys[activePage]
                        Column(
                            modifier = Modifier.weight(1f),
                            verticalArrangement = Arrangement.spacedBy(6.dp)
                        ) {
                            for (rowIdx in 0 until 4) {
                                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                    for (colIdx in 0 until 2) {
                                        val item = currentKeys[rowIdx * 2 + colIdx]
                                        val (bg, fg) = when (item.tone) {
                                            "red" -> p.red.copy(alpha = 0.22f) to p.red
                                            "orange" -> p.orange.copy(alpha = 0.22f) to p.orange
                                            "green" -> p.green.copy(alpha = 0.22f) to p.green
                                            "muted" -> keyBg to p.secondaryLabel
                                            else -> keyBg to p.label
                                        }
                                        MiniActionKey(item.label, bg, fg, Modifier.weight(1f)) {
                                            onSendKey(item.seq)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/**
 * Full virtual terminal keyboard with sticky Ctrl / Alt / Shift modifiers,
 * Fn (F1–F12, Home/End/PgUp/PgDn/Del), Symbol layer, full QWERTY + digits, and arrow keys.
 */
@Composable
private fun FullTerminalKeyboard(
    compact: Boolean,
    onSendKey: (String) -> Unit,
    onClose: () -> Unit
) {
    val p = LocalIosPalette.current
    // Modifier states: 0 = off, 1 = one-shot sticky, 2 = locked
    var ctrlState by remember { mutableIntStateOf(0) }
    var altState by remember { mutableIntStateOf(0) }
    var shiftState by remember { mutableIntStateOf(0) }
    var fnMode by remember { mutableStateOf(false) }
    var symMode by remember { mutableStateOf(false) }

    val kbBg = if (p.isDark) Color(0xFF141416) else Color(0xFFD5D7DD)
    val keyNormalBg = if (p.isDark) Color(0xFF2C2C2E) else Color(0xFFFFFFFF)
    val keyFuncBg = if (p.isDark) Color(0xFF202024) else Color(0xFFE4E6EC)
    val keyActiveBg = p.accent
    val keyLockBg = p.orange

    val keyH = if (compact) 31.dp else 42.dp
    val digitKeyH = if (compact) 29.dp else 38.dp
    val topKeyH = if (compact) 29.dp else 36.dp
    val rowGap = if (compact) 4.dp else 6.dp
    val keyGap = if (compact) 4.dp else 4.5.dp
    val topKeyGap = 6.dp

    fun consumeOneShotModifiers() {
        if (ctrlState == 1) ctrlState = 0
        if (altState == 1) altState = 0
        if (shiftState == 1) shiftState = 0
    }

    fun sendCharKey(baseChar: String, shiftedChar: String = baseChar.uppercase()) {
        val useShift = shiftState > 0
        val ch = if (useShift) shiftedChar else baseChar
        var seq = ch
        if (ctrlState > 0 && ch.length == 1) {
            val c = ch[0].lowercaseChar()
            seq = when {
                c in 'a'..'z' -> (c.code - 'a'.code + 1).toChar().toString()
                c == '[' -> "\u001B"
                c == '\\' -> "\u001C"
                c == ']' -> "\u001D"
                c == '^' || c == '6' -> "\u001E"
                c == '_' || c == '-' -> "\u001F"
                c == ' ' || c == '2' -> "\u0000"
                else -> ch
            }
        }
        if (altState > 0) {
            seq = "\u001B$seq"
        }
        onSendKey(seq)
        consumeOneShotModifiers()
    }

    fun sendArrow(codeChar: Char) {
        val seq = when {
            ctrlState > 0 && altState > 0 -> "\u001B[1;7$codeChar"
            ctrlState > 0 -> "\u001B[1;5$codeChar"
            altState > 0 -> "\u001B[1;3$codeChar"
            shiftState > 0 -> "\u001B[1;2$codeChar"
            else -> "\u001B[$codeChar"
        }
        onSendKey(seq)
        consumeOneShotModifiers()
    }

    fun sendSpecial(seq: String) {
        val out = if (altState > 0 && !seq.startsWith("\u001B")) "\u001B$seq" else seq
        onSendKey(out)
        consumeOneShotModifiers()
    }

    fun cycleSticky(cur: Int): Int = when (cur) {
        0 -> 1
        1 -> 2
        else -> 0
    }

    val statusHint = buildString {
        if (ctrlState == 2) append("Ctrl锁定 ") else if (ctrlState == 1) append("Ctrl+ ")
        if (altState == 2) append("Alt锁定 ") else if (altState == 1) append("Alt+ ")
        if (shiftState == 2) append("大写锁定 ") else if (shiftState == 1) append("Shift+ ")
        if (fnMode) append("Fn层 ")
        if (symMode) append("符号层 ")
    }.trim()

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(kbBg)
            .navigationBarsPadding()
            .padding(
                start = 8.dp,
                end = 8.dp,
                top = 8.dp,
                bottom = if (compact) 10.dp else 28.dp
            ),
        verticalArrangement = Arrangement.spacedBy(rowGap)
    ) {
        // Row 1 (6 spacious keys): Core control & sticky modifier keys (including Shift)
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(topKeyGap)
        ) {
            FullKbKey("Esc", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 13.sp) {
                sendSpecial("\u001B")
            }
            FullKbKey("Tab", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 13.sp) {
                if (shiftState > 0) {
                    sendSpecial("\u001B[Z")
                } else {
                    sendSpecial("\t")
                }
            }
            FullKbKey(
                label = if (shiftState == 2) "Shift*" else "Shift",
                bg = when (shiftState) {
                    2 -> keyLockBg
                    1 -> keyActiveBg
                    else -> keyFuncBg
                },
                fg = if (shiftState > 0) Color.White else p.label,
                modifier = Modifier.weight(1.1f),
                height = topKeyH,
                fontSize = 13.sp
            ) { shiftState = cycleSticky(shiftState) }
            FullKbKey(
                label = if (ctrlState == 2) "Ctrl*" else "Ctrl",
                bg = when (ctrlState) {
                    2 -> keyLockBg
                    1 -> keyActiveBg
                    else -> keyFuncBg
                },
                fg = if (ctrlState > 0) Color.White else p.label,
                modifier = Modifier.weight(1f),
                height = topKeyH,
                fontSize = 13.sp
            ) { ctrlState = cycleSticky(ctrlState) }
            FullKbKey(
                label = if (altState == 2) "Alt*" else "Alt",
                bg = when (altState) {
                    2 -> keyLockBg
                    1 -> keyActiveBg
                    else -> keyFuncBg
                },
                fg = if (altState > 0) Color.White else p.label,
                modifier = Modifier.weight(1f),
                height = topKeyH,
                fontSize = 13.sp
            ) { altState = cycleSticky(altState) }
            FullKbKey(
                label = "Fn",
                bg = if (fnMode) keyActiveBg else keyFuncBg,
                fg = if (fnMode) Color.White else p.label,
                modifier = Modifier.weight(0.95f),
                height = topKeyH,
                fontSize = 13.sp
            ) {
                fnMode = !fnMode
                if (fnMode) symMode = false
            }
        }

        // Row 2 (7 spacious keys): One-tap Shift+Tab + large arrow keys + high-freq CLI keys (or Fn Nav keys)
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(topKeyGap)
        ) {
            if (fnMode) {
                val fnNavRow = listOf(
                    "Home" to "\u001B[H",
                    "End" to "\u001B[F",
                    "PgUp" to "\u001B[5~",
                    "PgDn" to "\u001B[6~",
                    "Del" to "\u001B[3~",
                    "F11" to "\u001B[23~",
                    "F12" to "\u001B[24~"
                )
                fnNavRow.forEach { (label, seq) ->
                    FullKbKey(label, keyFuncBg, p.accent, Modifier.weight(1f), topKeyH, 12.sp) {
                        sendSpecial(seq)
                    }
                }
            } else {
                FullKbKey("⇧Tab", keyFuncBg, p.label, Modifier.weight(1.3f), topKeyH, 12.sp) {
                    sendSpecial("\u001B[Z")
                }
                FullKbKey("←", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 16.sp) { sendArrow('D') }
                FullKbKey("↓", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 16.sp) { sendArrow('B') }
                FullKbKey("↑", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 16.sp) { sendArrow('A') }
                FullKbKey("→", keyFuncBg, p.label, Modifier.weight(1f), topKeyH, 16.sp) { sendArrow('C') }
                FullKbKey("/", keyFuncBg, p.label, Modifier.weight(0.95f), topKeyH, 16.sp) { sendCharKey("/", "/") }
                FullKbKey("-", keyFuncBg, p.label, Modifier.weight(0.95f), topKeyH, 16.sp) { sendCharKey("-", "_") }
            }
        }

        // Row 3: Digits / Shifted Symbols / F1-F10
        val row3Digits = listOf("1", "2", "3", "4", "5", "6", "7", "8", "9", "0")
        val row3Shifted = listOf("!", "@", "#", "$", "%", "^", "&", "*", "(", ")")
        val row3Fn = listOf(
            "F1" to "\u001BOP",
            "F2" to "\u001BOQ",
            "F3" to "\u001BOR",
            "F4" to "\u001BOS",
            "F5" to "\u001B[15~",
            "F6" to "\u001B[17~",
            "F7" to "\u001B[18~",
            "F8" to "\u001B[19~",
            "F9" to "\u001B[20~",
            "F10" to "\u001B[21~"
        )
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(keyGap)
        ) {
            if (fnMode) {
                row3Fn.forEach { (label, seq) ->
                    FullKbKey(label, keyNormalBg, p.accent, Modifier.weight(1f), digitKeyH, 12.sp) {
                        sendSpecial(seq)
                    }
                }
            } else {
                row3Digits.indices.forEach { i ->
                    val display = if (shiftState > 0 || symMode) row3Shifted[i] else row3Digits[i]
                    FullKbKey(display, keyNormalBg, p.label, Modifier.weight(1f), digitKeyH, 16.sp) {
                        if (symMode) {
                            sendCharKey(row3Shifted[i], row3Shifted[i])
                        } else {
                            sendCharKey(row3Digits[i], row3Shifted[i])
                        }
                    }
                }
            }
        }

        // Row 4: QWERTY or Symbols
        val row4Letters = listOf("q", "w", "e", "r", "t", "y", "u", "i", "o", "p")
        val row4Symbols = listOf("`", "~", "-", "_", "=", "+", "[", "]", "{", "}")
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(keyGap)
        ) {
            if (fnMode || symMode) {
                row4Symbols.forEach { sym ->
                    FullKbKey(sym, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(sym, sym)
                    }
                }
            } else {
                row4Letters.forEach { ch ->
                    val disp = if (shiftState > 0) ch.uppercase() else ch
                    FullKbKey(disp, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(ch)
                    }
                }
            }
        }

        // Row 5: ASDFGHJKL or Symbols
        val row5Letters = listOf("a", "s", "d", "f", "g", "h", "j", "k", "l")
        val row5Symbols = listOf("\\", "|", ";", ":", "'", "\"", "<", ">", "/", "?")
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = if (fnMode || symMode) 0.dp else 10.dp),
            horizontalArrangement = Arrangement.spacedBy(keyGap)
        ) {
            if (fnMode || symMode) {
                row5Symbols.forEach { sym ->
                    FullKbKey(sym, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(sym, sym)
                    }
                }
            } else {
                row5Letters.forEach { ch ->
                    val disp = if (shiftState > 0) ch.uppercase() else ch
                    FullKbKey(disp, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(ch)
                    }
                }
            }
        }

        // Row 6: Shift + ZXCVBNM / Extra Symbols + Backspace
        val row6Letters = listOf("z", "x", "c", "v", "b", "n", "m")
        val row6Symbols = listOf(",", ".", "!", "?", "@", "#", "$")
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(keyGap)
        ) {
            FullKbKey(
                label = if (shiftState == 2) "⇪" else "⇧",
                bg = when (shiftState) {
                    2 -> keyLockBg
                    1 -> keyActiveBg
                    else -> keyFuncBg
                },
                fg = if (shiftState > 0) Color.White else p.label,
                modifier = Modifier.weight(1.5f),
                height = keyH,
                fontSize = 17.sp
            ) { shiftState = cycleSticky(shiftState) }

            if (fnMode || symMode) {
                row6Symbols.forEach { sym ->
                    FullKbKey(sym, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(sym, sym)
                    }
                }
            } else {
                row6Letters.forEach { ch ->
                    val disp = if (shiftState > 0) ch.uppercase() else ch
                    FullKbKey(disp, keyNormalBg, p.label, Modifier.weight(1f), keyH, 17.sp) {
                        sendCharKey(ch)
                    }
                }
            }

            FullKbKey("⌫", keyFuncBg, p.red, Modifier.weight(1.5f), keyH, 17.sp) {
                sendSpecial("\u007F")
            }
        }

        // Row 7 (5 spacious bottom keys): Symbol toggle + dot + wide Space + large Enter + Hide
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(topKeyGap)
        ) {
            FullKbKey(
                label = if (symMode) "ABC" else "符号",
                bg = if (symMode) keyActiveBg else keyFuncBg,
                fg = if (symMode) Color.White else p.label,
                modifier = Modifier.weight(1.45f),
                height = keyH,
                fontSize = 14.sp
            ) {
                symMode = !symMode
                if (symMode) fnMode = false
            }
            FullKbKey(".", keyNormalBg, p.label, Modifier.weight(1.0f), keyH, 18.sp) { sendCharKey(".", ".") }
            FullKbKey(
                label = statusHint.ifBlank { "Space" },
                bg = keyNormalBg,
                fg = if (statusHint.isNotBlank()) p.accent else p.secondaryLabel,
                modifier = Modifier.weight(3.6f),
                height = keyH,
                fontSize = 13.sp
            ) { sendCharKey(" ", " ") }
            FullKbKey(
                label = if (altState > 0) "Alt+⏎" else "⏎",
                bg = p.accent,
                fg = Color.White,
                modifier = Modifier.weight(1.75f),
                height = keyH,
                fontSize = 16.sp
            ) { sendSpecial("\r") }
            FullKbKey("收起", keyFuncBg, p.secondaryLabel, Modifier.weight(1.3f), keyH, 13.sp) {
                onClose()
            }
        }
    }
}

@Composable
private fun FullKbKey(
    label: String,
    bg: Color,
    fg: Color,
    modifier: Modifier,
    height: androidx.compose.ui.unit.Dp,
    fontSize: androidx.compose.ui.unit.TextUnit,
    onClick: () -> Unit
) {
    Surface(
        onClick = onClick,
        color = bg,
        shape = RoundedCornerShape(7.dp),
        shadowElevation = 0.5.dp,
        modifier = modifier.height(height)
    ) {
        Box(contentAlignment = Alignment.Center, modifier = Modifier.fillMaxSize()) {
            Text(
                text = label,
                color = fg,
                fontSize = fontSize,
                fontWeight = FontWeight.Medium,
                textAlign = TextAlign.Center,
                maxLines = 1
            )
        }
    }
}

@Composable
private fun PadKey(
    label: String,
    bg: Color,
    fg: Color,
    w: androidx.compose.ui.unit.Dp,
    h: androidx.compose.ui.unit.Dp,
    circle: Boolean = false,
    onClick: () -> Unit
) {
    Surface(
        onClick = onClick,
        color = bg,
        shape = if (circle) CircleShape else RoundedCornerShape(10.dp),
        modifier = Modifier.size(width = w, height = h)
    ) {
        Box(contentAlignment = Alignment.Center) {
            Text(label, color = fg, fontSize = 17.sp, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun MiniActionKey(label: String, bg: Color, fg: Color, modifier: Modifier, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        color = bg,
        shape = RoundedCornerShape(9.dp),
        modifier = modifier.height(36.dp)
    ) {
        Box(contentAlignment = Alignment.Center, modifier = Modifier.padding(horizontal = 4.dp)) {
            Text(
                text = label,
                color = fg,
                fontSize = 12.sp,
                fontWeight = FontWeight.SemiBold,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }
    }
}

@Composable
private fun SmallTermChip(label: String, bg: Color, fg: Color, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        color = bg,
        shape = RoundedCornerShape(8.dp)
    ) {
        Text(
            text = label,
            color = fg,
            fontSize = 12.sp,
            fontWeight = FontWeight.Medium,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 5.dp)
        )
    }
}

@Composable
private fun TmuxSessionRow(
    session: TmuxSession,
    enabled: Boolean,
    onAttach: () -> Unit,
    onEditNote: () -> Unit,
    onKill: () -> Unit
) {
    val p = LocalIosPalette.current
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(enabled = enabled) { onAttach() }
            .padding(start = 16.dp, end = 8.dp, top = 11.dp, bottom = 11.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Box(modifier = Modifier.size(8.dp).clip(CircleShape).background(p.green))
                Text(
                    text = session.note.ifBlank { session.name },
                    color = p.label,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 15.sp,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
                if (session.agent.isNotBlank()) {
                    AgentBadge(session.agent)
                }
            }
            Text(
                text = buildString {
                    if (session.note.isNotBlank()) append("${session.name} · ")
                    if (session.projectName.isNotBlank()) append(session.projectName)
                    if (session.command.isNotBlank()) {
                        if (isNotEmpty() && !endsWith("· ")) append(" · ")
                        append(session.command)
                    }
                }.ifBlank { session.cwd },
                color = p.secondaryLabel,
                fontSize = 12.sp,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onEditNote, modifier = Modifier.size(36.dp)) {
                Icon(Icons.Default.Edit, contentDescription = "打备注", tint = p.accent, modifier = Modifier.size(18.dp))
            }
            IconButton(onClick = onKill, enabled = enabled, modifier = Modifier.size(36.dp)) {
                Icon(Icons.Default.DeleteOutline, contentDescription = "关闭终端", tint = p.red, modifier = Modifier.size(19.dp))
            }
        }
    }
}
