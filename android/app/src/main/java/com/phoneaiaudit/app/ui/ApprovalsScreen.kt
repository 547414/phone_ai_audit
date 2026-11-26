package com.phoneaiaudit.app.ui

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
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.filled.VerifiedUser
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.fragment.app.FragmentActivity
import com.phoneaiaudit.app.data.ApprovalItem
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.agentDisplayName
import com.phoneaiaudit.app.notification.AuditNotifier
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@Composable
fun ApprovalsScreen(
    activity: FragmentActivity,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val pending by AuditRepository.pendingApprovals.collectAsState()
    val recent by AuditRepository.recentApprovals.collectAsState()
    val status by AuditRepository.status.collectAsState()
    val autoApproveOnTimeout = status.settings.timeoutAction == "approve"
    val scope = rememberCoroutineScope()
    val context = LocalContext.current

    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(pending.isNotEmpty()) {
        while (pending.isNotEmpty()) {
            nowMs = System.currentTimeMillis()
            delay(1000)
        }
    }

    var denyTarget by remember { mutableStateOf<ApprovalItem?>(null) }
    var denyReason by remember { mutableStateOf("") }
    var busyId by remember { mutableStateOf<String?>(null) }

    fun submitDecision(item: ApprovalItem, decision: String, reason: String = "") {
        val runAction = {
            busyId = item.id
            scope.launch {
                val res = AuditRepository.decideApproval(item.id, decision, reason)
                busyId = null
                AuditNotifier.cancelApproval(context, item.id)
                res.onFailure { onShowSnack("提交失败: ${it.message}") }
            }
        }
        if (decision == "approved" && item.isHighRisk && AuditRepository.config.biometricForHighRisk) {
            BiometricHelper.authenticate(
                activity = activity,
                title = "确认高危操作",
                subtitle = item.summary,
                onSuccess = { runAction() },
                onError = { onShowSnack("已取消高危验证: $it") }
            )
        } else {
            runAction()
        }
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
        IosPageHeader(
            title = "审批",
            subtitle = if (pending.isEmpty()) "无待处理权限请求" else "${pending.size} 项等待你确认",
            trailing = {
                IconButton(onClick = {
                    scope.launch {
                        val res = AuditRepository.refreshAll()
                        res.onSuccess { onShowSnack("刷新成功") }
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
            if (pending.isEmpty()) {
                item {
                    IosSection {
                        Column(
                            modifier = Modifier.fillMaxWidth().padding(vertical = 30.dp, horizontal = 20.dp),
                            horizontalAlignment = Alignment.CenterHorizontally
                        ) {
                            Icon(
                                Icons.Default.VerifiedUser,
                                contentDescription = null,
                                tint = p.green,
                                modifier = Modifier.size(36.dp)
                            )
                            Spacer(Modifier.height(10.dp))
                            Text("当前全部清空", color = p.label, fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
                            Spacer(Modifier.height(4.dp))
                            Text(
                                "Claude Code / Codex / Antigravity 请求权限时会实时弹出",
                                color = p.secondaryLabel,
                                fontSize = 13.sp
                            )
                        }
                    }
                }
            } else {
                items(pending, key = { it.id }) { item ->
                    PendingApprovalCard(
                        item = item,
                        nowMs = nowMs,
                        busy = busyId == item.id,
                        autoApproveOnTimeout = autoApproveOnTimeout,
                        onApprove = { submitDecision(item, "approved") },
                        onDeny = {
                            denyReason = ""
                            denyTarget = item
                        },
                        onFallback = { submitDecision(item, "fallback", "手机端选择退回电脑处理") },
                        onOpenTerminal = {
                            if (item.tmuxSession.isNotBlank()) {
                                AuditRepository.requestOpenTerminal("tmux_attach", item.tmuxSession)
                            } else {
                                AuditRepository.selectTab(1)
                            }
                        }
                    )
                }
            }

            if (recent.isNotEmpty()) {
                item {
                    IosSection(title = "近期审批记录") {
                        recent.take(20).forEachIndexed { idx, item ->
                            HistoryApprovalRow(item)
                            if (idx < recent.take(20).lastIndex) {
                                IosDivider(startIndent = 16.dp)
                            }
                        }
                    }
                }
            }
        }
    }

    denyTarget?.let { target ->
        val presets = listOf("先不要执行，换个更安全方案", "请改在测试环境操作", "先给我看修改计划")
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { denyTarget = null },
            title = { Text("拒绝并给 AI 反馈", fontWeight = FontWeight.SemiBold) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(target.summary, color = p.secondaryLabel, fontSize = 13.sp, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    OutlinedTextField(
                        value = denyReason,
                        onValueChange = { denyReason = it },
                        label = { Text("拒绝理由（可选，会回传给 AI）", fontSize = 13.sp) },
                        singleLine = false,
                        maxLines = 3,
                        modifier = Modifier.fillMaxWidth()
                    )
                    Row(
                        modifier = Modifier.horizontalScroll(rememberScrollState()),
                        horizontalArrangement = Arrangement.spacedBy(6.dp)
                    ) {
                        presets.forEach { preset ->
                            AssistChip(onClick = { denyReason = preset }, label = { Text(preset, fontSize = 11.sp) })
                        }
                    }
                }
            },
            confirmButton = {
                IosButton(
                    text = "确认拒绝",
                    containerColor = p.red,
                    onClick = {
                        val t = target
                        val r = denyReason
                        denyTarget = null
                        submitDecision(t, "denied", r)
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { denyTarget = null }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }
}

@Composable
internal fun PendingApprovalCard(
    item: ApprovalItem,
    nowMs: Long,
    busy: Boolean,
    autoApproveOnTimeout: Boolean = true,
    onApprove: () -> Unit,
    onDeny: () -> Unit,
    onFallback: () -> Unit,
    onOpenTerminal: () -> Unit
) {
    val p = LocalIosPalette.current
    var expanded by remember { mutableStateOf(false) }
    val totalMs = (item.expiresAt - item.createdAt).coerceAtLeast(1000L)
    val leftMs = (item.expiresAt - nowMs).coerceAtLeast(0L)
    val progress = (leftMs.toFloat() / totalMs.toFloat()).coerceIn(0f, 1f)
    val leftSec = ((leftMs + 999L) / 1000L).toInt().coerceAtLeast(0)
    val accent = if (item.isHighRisk) p.red else if (autoApproveOnTimeout) p.green else p.accent
    val countdownText = if (autoApproveOnTimeout) "${leftSec}s 自动允许" else "${leftSec}s"
    val countdownColor = when {
        autoApproveOnTimeout && leftSec <= 10 -> p.orange
        autoApproveOnTimeout -> p.green
        leftSec <= 10 -> p.red
        else -> accent
    }

    Surface(
        modifier = Modifier.fillMaxWidth(),
        shape = RoundedCornerShape(16.dp),
        color = p.card,
        border = BorderStroke(
            width = if (item.isHighRisk) 1.dp else 0.5.dp,
            color = if (item.isHighRisk) p.red.copy(alpha = 0.6f) else p.separator.copy(alpha = 0.55f)
        )
    ) {
        Column(modifier = Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                    modifier = Modifier.weight(1f)
                ) {
                    AgentBadge(item.agent)
                    if (item.isHighRisk) {
                        IosPillBadge("高危", p.red)
                    }
                    if (item.projectName.isNotBlank()) {
                        Text(
                            item.projectName,
                            color = p.secondaryLabel,
                            fontSize = 12.sp,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis
                        )
                    }
                }
                Text(
                    countdownText,
                    color = countdownColor,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Bold
                )
            }

            LinearProgressIndicator(
                progress = { progress },
                modifier = Modifier.fillMaxWidth().height(3.dp).clip(RoundedCornerShape(2.dp)),
                color = accent,
                trackColor = p.secondaryFill
            )

            if (item.taskTitle.isNotBlank()) {
                Text(
                    text = item.taskTitle,
                    color = p.secondaryLabel,
                    fontSize = 13.sp,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }

            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(10.dp))
                    .background(p.codeBg)
                    .clickable { expanded = !expanded }
                    .padding(12.dp)
            ) {
                Text(
                    text = item.summary,
                    color = p.codeFg,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                    maxLines = if (expanded) Int.MAX_VALUE else 1,
                    overflow = TextOverflow.Ellipsis
                )
            }

            if (item.riskReasons.isNotEmpty()) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                    Icon(
                        Icons.Default.Warning,
                        contentDescription = null,
                        tint = if (item.isHighRisk) p.red else p.orange,
                        modifier = Modifier.size(14.dp)
                    )
                    Text(
                        text = item.riskReasons.joinToString(" · "),
                        color = if (item.isHighRisk) p.red else p.orange,
                        fontSize = 12.sp
                    )
                }
            }

            val targetFile = remember(item.payloadJson) { item.extractTargetFilePath() }
            if (targetFile != null && targetFile.startsWith("/")) {
                Text(
                    text = "查看目标文件: ${targetFile.substringAfterLast('/')} →",
                    color = p.accent,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.clickable { AuditRepository.requestOpenFile(targetFile) }.padding(vertical = 2.dp)
                )
            }

            if (item.payloadJson.isNotBlank()) {
                Text(
                    text = if (expanded) "收起完整参数 ▲" else "展开完整参数 ▼",
                    color = p.accent,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.clickable { expanded = !expanded }.padding(vertical = 2.dp)
                )
                if (expanded) {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clip(RoundedCornerShape(10.dp))
                            .background(p.codeBg)
                            .padding(10.dp)
                    ) {
                        Text(
                            text = item.payloadJson.take(2000),
                            color = p.secondaryLabel,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            lineHeight = 15.sp
                        )
                    }
                }
            }

            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                IosButton(
                    text = if (item.isHighRisk) "验证允许" else "允许",
                    onClick = onApprove,
                    enabled = !busy,
                    containerColor = p.green,
                    icon = Icons.Default.Check,
                    modifier = Modifier.weight(1f)
                )
                IosButton(
                    text = "拒绝",
                    onClick = onDeny,
                    enabled = !busy,
                    containerColor = p.red,
                    icon = Icons.Default.Close,
                    modifier = Modifier.weight(1f)
                )
            }

            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                IosTintedButton(
                    text = "退回电脑",
                    onClick = onFallback,
                    enabled = !busy,
                    tint = p.secondaryLabel,
                    icon = Icons.Default.Computer,
                    modifier = Modifier.weight(1f)
                )
                IosTintedButton(
                    text = if (item.tmuxSession.isNotBlank()) "进终端" else "查看终端",
                    onClick = onOpenTerminal,
                    tint = p.accent,
                    icon = Icons.Default.Terminal,
                    modifier = Modifier.weight(1f)
                )
            }
        }
    }
}

@Composable
internal fun HistoryApprovalRow(item: ApprovalItem, onClick: (() -> Unit)? = null) {
    val p = LocalIosPalette.current
    val (label, color) = when {
        item.status == "approved" && (item.decidedBy == "system_timeout" || item.decisionReason.contains("超时")) -> "超时自动允许" to p.green
        item.status == "approved" && item.decidedBy == "mac_local" -> "电脑已允许" to p.green
        item.status == "approved" -> "已允许" to p.green
        item.status == "denied" -> "已拒绝" to p.red
        item.status == "fallback" -> "退回电脑" to p.orange
        item.status == "expired" -> "超时拒绝" to p.red
        item.status == "canceled" -> "已取消" to p.secondaryLabel
        else -> item.status to p.secondaryLabel
    }
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 16.dp, vertical = 12.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                AgentBadge(item.agent)
                Text(label, color = color, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
            }
            Text(formatTime(item.createdAt), fontSize = 11.sp, color = p.tertiaryLabel)
        }
        Text(
            item.summary,
            color = p.label,
            fontSize = 13.sp,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis
        )
        if (item.decisionReason.isNotBlank()) {
            Text(
                "备注: ${item.decisionReason}",
                fontSize = 12.sp,
                color = p.secondaryLabel,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }
    }
}

@Composable
fun AgentBadge(agent: String) {
    val color = when (agent.lowercase()) {
        "claude" -> Color(0xFFD97706)
        "codex" -> Color(0xFF10B981)
        "agy" -> Color(0xFF0A84FF)
        else -> Color(0xFF8E8E93)
    }
    IosPillBadge(text = agentDisplayName(agent), color = color)
}

fun formatTime(ms: Long): String {
    if (ms <= 0L) return ""
    return SimpleDateFormat("MM-dd HH:mm:ss", Locale.getDefault()).format(Date(ms))
}
