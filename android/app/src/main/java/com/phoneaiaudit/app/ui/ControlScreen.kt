package com.phoneaiaudit.app.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.app.NotificationManagerCompat
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.ConnectionState
import com.phoneaiaudit.app.notification.AuditNotifier
import com.phoneaiaudit.app.service.AuditForegroundService
import kotlinx.coroutines.launch
import org.json.JSONObject

@Composable
fun ControlScreen(
    onScanQr: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val connState by AuditRepository.connectionState.collectAsState()
    val status by AuditRepository.status.collectAsState()
    val lastError by AuditRepository.lastError.collectAsState()
    val cfgVer by AuditRepository.configVersion.collectAsState()
    val themeMode by AuditRepository.themeMode.collectAsState()

    var serverUrl by remember(cfgVer) { mutableStateOf(AuditRepository.config.serverUrl) }
    var appToken by remember(cfgVer) { mutableStateOf(AuditRepository.config.appToken) }
    var bioTerminal by remember { mutableStateOf(AuditRepository.config.biometricForTerminal) }
    var bioHighRisk by remember { mutableStateOf(AuditRepository.config.biometricForHighRisk) }
    var bioFiles by remember { mutableStateOf(AuditRepository.config.biometricForFiles) }
    var customTimeoutText by remember(status.settings.defaultTimeoutSec) {
        mutableStateOf(status.settings.defaultTimeoutSec.toString())
    }

    val notifEnabled = NotificationManagerCompat.from(context).areNotificationsEnabled()
    val pm = context.getSystemService(Context.POWER_SERVICE) as PowerManager
    val batteryIgnored = pm.isIgnoringBatteryOptimizations(context.packageName)

    fun patchRemote(key: String, value: Any, successMsg: String? = null) {
        scope.launch {
            val res = AuditRepository.updateSettings(JSONObject().put(key, value))
            res.onSuccess { if (!successMsg.isNullOrBlank()) onShowSnack(successMsg) }
                .onFailure { onShowSnack("同步设置失败: ${it.message}") }
        }
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
        IosPageHeader(title = "控制", subtitle = "连接状态、审批策略与系统设置")

        LazyColumn(
            modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(18.dp)
        ) {
            // 1. Live Connection Status (Full Width!)
            item {
                val (connText, connColor) = when (connState) {
                    ConnectionState.CONNECTED -> "中转通道在线" to p.green
                    ConnectionState.CONNECTING -> "正在连接中转..." to p.orange
                    ConnectionState.DISCONNECTED -> "未连接" to p.red
                }
                IosSection(title = "连接状态") {
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(16.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Box(modifier = Modifier.size(10.dp).clip(CircleShape).background(connColor))
                                Text(connText, color = p.label, fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
                            }
                            IosPillBadge(
                                text = if (status.macOnline) "Mac 在线" else "Mac 离线",
                                color = if (status.macOnline) p.green else p.secondaryLabel
                            )
                        }
                        Text(
                            text = if (status.macOnline)
                                "${status.macHostname.ifBlank { "Mac" }} · 活跃 tmux 会话 ${status.tmuxSessions.size} 个"
                            else
                                "请确认 Mac 已运行 ai-audit-agent",
                            color = p.secondaryLabel,
                            fontSize = 13.sp
                        )
                        if (!lastError.isNullOrBlank() && connState != ConnectionState.CONNECTED) {
                            Text("原因: $lastError", color = p.red, fontSize = 12.sp)
                        }
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            IosTintedButton(
                                text = "重新连接",
                                onClick = {
                                    AuditForegroundService.start(context, reconnect = true)
                                    onShowSnack("正在重新连接...")
                                },
                                modifier = Modifier.weight(1f)
                            )
                            IosTintedButton(
                                text = "刷新状态",
                                icon = Icons.Default.Refresh,
                                tint = p.secondaryLabel,
                                onClick = {
                                    scope.launch {
                                        AuditRepository.refreshAll()
                                            .onSuccess { onShowSnack("刷新成功") }
                                            .onFailure { onShowSnack("刷新失败: ${it.message}") }
                                    }
                                },
                                modifier = Modifier.weight(1f)
                            )
                        }
                    }
                }
            }

            // 2. Theme Appearance (Light / Dark / System)
            item {
                IosSection(title = "外观主题") {
                    Column(modifier = Modifier.fillMaxWidth().padding(16.dp)) {
                        IosSegmentedControl(
                            options = listOf("system" to "跟随系统", "light" to "浅色", "dark" to "深色"),
                            selectedKey = themeMode,
                            onSelect = { AuditRepository.setThemeMode(it) }
                        )
                    }
                }
            }

            // 3. Remote Approval Mode & Switches
            item {
                val modeFooter = when (status.settings.approvalMode) {
                    "auto" -> "人在电脑前直接走本地弹窗；离座超过 ${status.settings.idleThresholdSec} 秒或锁屏自动转手机审批。"
                    "both" -> "电脑屏幕右上角浮窗与手机通知栏同时弹出审批，哪边先点以哪边为准，另一端自动消失。"
                    "phone" -> "所有权限请求统一优先发送至手机审批。"
                    else -> "不阻塞终端，仅接收任务完成/出错/提醒通知。"
                }
                IosSection(title = "审批模式", footer = modeFooter) {
                    Column(modifier = Modifier.fillMaxWidth().padding(16.dp)) {
                        IosSegmentedControl(
                            options = listOf(
                                "auto" to "智能离座",
                                "both" to "双端同步",
                                "phone" to "始终手机",
                                "notify" to "仅通知"
                            ),
                            selectedKey = status.settings.approvalMode,
                            onSelect = { patchRemote("approval_mode", it) }
                        )
                    }
                }
            }

            // 3.5 Approval Timeout & Auto-Approve Settings (synced to Mac & Phone)
            item {
                val curSec = status.settings.defaultTimeoutSec
                val autoApprove = status.settings.timeoutAction == "approve"
                val timeoutFooter = if (autoApprove) {
                    "当前设置：弹窗超过 $curSec 秒未手动操作将自动确认允许，Mac 浮窗与手机通知同时消除并标为已读。"
                } else {
                    "当前设置：弹窗超过 $curSec 秒未手动操作不会自动允许，将自动退回电脑终端处理。"
                }
                val timeoutFieldColors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = p.accent,
                    unfocusedBorderColor = p.separator,
                    focusedTextColor = p.label,
                    unfocusedTextColor = p.label,
                    focusedLabelColor = p.accent,
                    unfocusedLabelColor = p.secondaryLabel
                )
                IosSection(title = "审批超时设置（Mac / 手机同步生效）", footer = timeoutFooter) {
                    IosSwitchRow(
                        title = "超时自动确认授权",
                        subtitle = "默认开启；超时自动允许，并同步消除电脑和手机弹窗、标为已读",
                        checked = autoApprove
                    ) { enabled ->
                        patchRemote(
                            "timeout_action",
                            if (enabled) "approve" else "fallback",
                            if (enabled) "已开启超时自动确认授权" else "已关闭超时自动确认授权"
                        )
                    }
                    IosDivider()
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(16.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Text("授权弹窗超时时长", color = p.label, fontSize = 15.sp, fontWeight = FontWeight.Medium)
                            Text(
                                "当前 ${curSec} 秒",
                                color = p.accent,
                                fontSize = 13.sp,
                                fontWeight = FontWeight.SemiBold
                            )
                        }
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .horizontalScroll(rememberScrollState()),
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            listOf(15, 30, 60, 120, 180, 300).forEach { sec ->
                                val selected = curSec == sec
                                Surface(
                                    shape = RoundedCornerShape(10.dp),
                                    color = if (selected) p.accent else p.secondaryFill,
                                    border = if (selected) null else BorderStroke(0.5.dp, p.separator.copy(alpha = 0.6f)),
                                    modifier = Modifier.clickable {
                                        customTimeoutText = sec.toString()
                                        patchRemote("default_timeout_sec", sec, "超时时长已同步为 ${sec} 秒")
                                    }
                                ) {
                                    Text(
                                        text = "${sec}秒",
                                        color = if (selected) Color.White else p.label,
                                        fontSize = 13.sp,
                                        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
                                        modifier = Modifier.padding(horizontal = 12.dp, vertical = 7.dp)
                                    )
                                }
                            }
                        }
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            OutlinedTextField(
                                value = customTimeoutText,
                                onValueChange = { input ->
                                    customTimeoutText = input.filter { it.isDigit() }.take(3)
                                },
                                label = { Text("自定义秒数 (5-600)", fontSize = 12.sp) },
                                singleLine = true,
                                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                                shape = RoundedCornerShape(10.dp),
                                colors = timeoutFieldColors,
                                modifier = Modifier.weight(1f)
                            )
                            IosButton(
                                text = "应用",
                                onClick = {
                                    val sec = customTimeoutText.trim().toIntOrNull()
                                    if (sec == null || sec !in 5..600) {
                                        onShowSnack("请输入 5 到 600 之间的整数秒")
                                    } else {
                                        patchRemote("default_timeout_sec", sec, "超时时长已同步为 ${sec} 秒")
                                    }
                                },
                                modifier = Modifier.width(84.dp)
                            )
                        }
                    }
                }
            }

            item {
                IosSection(title = "安全与控制开关") {
                    IosSwitchRow(
                        title = "允许手机连接 Mac 终端",
                        subtitle = "关闭后拒绝所有远程 Shell 连接",
                        checked = status.settings.terminalEnabled
                    ) { patchRemote("terminal_enabled", it) }
                    IosDivider()
                    IosSwitchRow(
                        title = "允许远程查看/编辑 Mac 文件",
                        subtitle = "访问已配置的 Mac 沙箱目录，含 Git Diff / Commit / Push",
                        checked = status.settings.filesEnabled
                    ) { patchRemote("files_enabled", it) }
                    IosDivider()
                    IosSwitchRow(
                        title = "Mac 防休眠 (caffeinate)",
                        subtitle = "防止合盖或无人操作时断网",
                        checked = status.settings.macAwakeEnabled
                    ) { patchRemote("mac_awake_enabled", it) }
                    IosDivider()
                    IosSwitchRow(
                        title = "高危命令禁止通知栏盲点",
                        subtitle = "rm -rf / force push 等必须进 App 确认",
                        checked = status.settings.highRiskRequireApp
                    ) { patchRemote("high_risk_require_app", it) }
                    IosDivider()
                    IosSwitchRow(
                        title = "高危批准需要指纹/面容",
                        checked = bioHighRisk
                    ) {
                        bioHighRisk = it
                        AuditRepository.config.biometricForHighRisk = it
                    }
                    IosDivider()
                    IosSwitchRow(
                        title = "进入终端需要指纹/面容",
                        checked = bioTerminal
                    ) {
                        bioTerminal = it
                        AuditRepository.config.biometricForTerminal = it
                    }
                    IosDivider()
                    IosSwitchRow(
                        title = "进入文件浏览器需要指纹/面容",
                        checked = bioFiles
                    ) {
                        bioFiles = it
                        AuditRepository.config.biometricForFiles = it
                    }
                }
            }

            // 4. Server & QR Pairing
            item {
                val fieldColors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = p.accent,
                    unfocusedBorderColor = p.separator,
                    focusedTextColor = p.label,
                    unfocusedTextColor = p.label,
                    focusedLabelColor = p.accent,
                    unfocusedLabelColor = p.secondaryLabel
                )
                IosSection(
                    title = "服务器与配对",
                    footer = "在 Mac 终端执行 ai-audit pair 即可展示配对二维码。"
                ) {
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(16.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        OutlinedTextField(
                            value = serverUrl,
                            onValueChange = { serverUrl = it },
                            label = { Text("中转地址") },
                            placeholder = { Text("https://audit.example.com:9876") },
                            singleLine = true,
                            shape = RoundedCornerShape(10.dp),
                            colors = fieldColors,
                            modifier = Modifier.fillMaxWidth()
                        )
                        OutlinedTextField(
                            value = appToken,
                            onValueChange = { appToken = it },
                            label = { Text("App Token") },
                            singleLine = true,
                            shape = RoundedCornerShape(10.dp),
                            colors = fieldColors,
                            modifier = Modifier.fillMaxWidth()
                        )
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            IosTintedButton(
                                text = "扫码配对",
                                icon = Icons.Default.QrCodeScanner,
                                onClick = onScanQr,
                                modifier = Modifier.weight(1f)
                            )
                            IosButton(
                                text = "保存并连接",
                                onClick = {
                                    AuditRepository.config.serverUrl = serverUrl
                                    AuditRepository.config.appToken = appToken
                                    AuditRepository.notifyConfigChanged()
                                    AuditForegroundService.start(context, reconnect = true)
                                    onShowSnack("配置已保存并重新连接")
                                },
                                modifier = Modifier.weight(1f)
                            )
                        }
                    }
                }
            }

            // 5. Android Background Keep-Alive Diagnostics
            item {
                IosSection(
                    title = "Android 后台保活体检",
                    footer = "建议打开「自启动」「后台高耗电」「锁屏通知横幅」；点击「隐藏保活通知」可单独关掉“后台保活”通知类别，完全隐藏常驻条且不影响审批推送。"
                ) {
                    CheckItemRow("系统通知权限", notifEnabled)
                    IosDivider()
                    CheckItemRow("忽略电池优化（后台高耗电）", batteryIgnored)
                    IosDivider(startIndent = 0.dp)
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(12.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            IosTintedButton(
                                text = "通知设置",
                                onClick = {
                                    val intent = Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).apply {
                                        putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName)
                                    }
                                    runCatching { context.startActivity(intent) }
                                },
                                modifier = Modifier.weight(1f)
                            )
                            IosTintedButton(
                                text = "忽略省电",
                                onClick = {
                                    val intent = Intent(
                                        Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                        Uri.parse("package:${context.packageName}")
                                    )
                                    runCatching { context.startActivity(intent) }
                                },
                                modifier = Modifier.weight(1f)
                            )
                            IosTintedButton(
                                text = "自启动",
                                tint = p.secondaryLabel,
                                onClick = {
                                    val intent = Intent(
                                        Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                        Uri.parse("package:${context.packageName}")
                                    )
                                    runCatching { context.startActivity(intent) }
                                },
                                modifier = Modifier.weight(1f)
                            )
                        }
                        IosTintedButton(
                            text = "彻底隐藏「在线」常驻保活通知（不影响审批推送）",
                            tint = p.secondaryLabel,
                            onClick = {
                                val intent = Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS).apply {
                                    putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName)
                                    putExtra(Settings.EXTRA_CHANNEL_ID, AuditNotifier.CHANNEL_SERVICE)
                                }
                                runCatching { context.startActivity(intent) }
                            },
                            modifier = Modifier.fillMaxWidth()
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun CheckItemRow(title: String, ok: Boolean) {
    val p = LocalIosPalette.current
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 13.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(title, color = p.label, fontSize = 14.sp)
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            Icon(
                imageVector = if (ok) Icons.Default.CheckCircle else Icons.Default.Warning,
                contentDescription = null,
                tint = if (ok) p.green else p.orange,
                modifier = Modifier.size(16.dp)
            )
            Text(
                text = if (ok) "已开启" else "待开启",
                color = if (ok) p.green else p.orange,
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium
            )
        }
    }
}
