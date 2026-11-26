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
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.DoneAll
import androidx.compose.material.icons.filled.NotificationsNone
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.VerifiedUser
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.fragment.app.FragmentActivity
import com.phoneaiaudit.app.data.ApprovalItem
import com.phoneaiaudit.app.data.AuditMessage
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.notification.AuditNotifier
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

@Composable
fun MessagesScreen(
    activity: FragmentActivity,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    val pendingAll by AuditRepository.pendingApprovals.collectAsState()
    val recentAll by AuditRepository.recentApprovals.collectAsState()
    val allMessages by AuditRepository.messages.collectAsState()
    val status by AuditRepository.status.collectAsState()
    val autoApproveOnTimeout = status.settings.timeoutAction == "approve"

    // Primary category: "all" (全部), "approval" (审批), "notice" (通知)
    var category by remember { mutableStateOf("all") }
    // Agent filter: "all", "claude", "codex", "agy"
    var filterAgent by remember { mutableStateOf("all") }

    var detailMsg by remember { mutableStateOf<AuditMessage?>(null) }
    var detailApproval by remember { mutableStateOf<ApprovalItem?>(null) }
    var denyTarget by remember { mutableStateOf<ApprovalItem?>(null) }
    var denyReason by remember { mutableStateOf("") }
    var busyId by remember { mutableStateOf<String?>(null) }

    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(pendingAll.isNotEmpty()) {
        while (pendingAll.isNotEmpty()) {
            nowMs = System.currentTimeMillis()
            delay(1000)
        }
    }

    val pending = remember(pendingAll, filterAgent) {
        if (filterAgent == "all") pendingAll
        else pendingAll.filter { it.agent.equals(filterAgent, ignoreCase = true) }
    }
    val recent = remember(recentAll, filterAgent) {
        if (filterAgent == "all") recentAll
        else recentAll.filter { it.agent.equals(filterAgent, ignoreCase = true) }
    }
    val filteredMessages = remember(allMessages, category, filterAgent) {
        allMessages.filter { m ->
            val agentOk = filterAgent == "all" || m.agent.equals(filterAgent, ignoreCase = true)
            val catOk = when (category) {
                "notice" -> m.eventType != "approval_result"
                "approval" -> m.eventType == "approval_result"
                else -> true
            }
            agentOk && catOk
        }
    }

    val unreadCount = remember(allMessages) { allMessages.count { it.readAt == 0L } }

    // Automatically mark viewed unread messages as read when user enters/views the Messages tab
    val visibleUnreadIds = remember(filteredMessages, category) {
        if (category == "approval") emptyList()
        else filteredMessages.filter { it.readAt == 0L }.map { it.id }
    }
    LaunchedEffect(visibleUnreadIds) {
        if (visibleUnreadIds.isNotEmpty()) {
            delay(650)
            AuditRepository.markMessagesRead(visibleUnreadIds)
            visibleUnreadIds.forEach { AuditNotifier.cancelMessage(context, it) }
        }
    }

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

    val headerSubtitle = when {
        pendingAll.isNotEmpty() && unreadCount > 0 -> "${pendingAll.size} 项待审批 · $unreadCount 条未读"
        pendingAll.isNotEmpty() -> "${pendingAll.size} 项待你审批 · 带绿点可直达终端"
        unreadCount > 0 -> "$unreadCount 条未读 · 带绿点可直达终端"
        else -> "全部已处理 · 带绿点可直达终端"
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
        IosPageHeader(
            title = "消息",
            subtitle = headerSubtitle,
            trailing = {
                IconButton(onClick = {
                    scope.launch {
                        AuditRepository.markAllMessagesRead()
                            .onSuccess { onShowSnack("已全部标记为已读") }
                            .onFailure { onShowSnack("操作失败: ${it.message}") }
                    }
                }) {
                    Icon(Icons.Default.DoneAll, contentDescription = "全部已读", tint = p.accent)
                }
                IconButton(onClick = {
                    scope.launch {
                        AuditRepository.refreshAll()
                            .onSuccess { onShowSnack("刷新成功") }
                            .onFailure { onShowSnack("刷新失败: ${it.message}") }
                    }
                }) {
                    Icon(Icons.Default.Refresh, contentDescription = "刷新", tint = p.accent)
                }
            }
        )

        // Category segmented control
        Box(modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp)) {
            val approvalLabel = if (pendingAll.isNotEmpty()) "审批 (${pendingAll.size})" else "审批"
            IosSegmentedControl(
                options = listOf("all" to "全部", "approval" to approvalLabel, "notice" to "通知"),
                selectedKey = category,
                onSelect = { category = it }
            )
        }

        // Compact AI filter chips
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .horizontalScroll(rememberScrollState())
                .padding(horizontal = 16.dp, vertical = 4.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            listOf("all" to "全部 AI", "claude" to "Claude", "codex" to "Codex", "agy" to "AGY").forEach { (key, label) ->
                val selected = filterAgent == key
                Surface(
                    shape = RoundedCornerShape(10.dp),
                    color = if (selected) p.accent else p.card,
                    border = if (selected) null else BorderStroke(0.5.dp, p.separator.copy(alpha = 0.6f)),
                    modifier = Modifier.clickable { filterAgent = key }
                ) {
                    Text(
                        text = label,
                        color = if (selected) Color.White else p.secondaryLabel,
                        fontSize = 12.sp,
                        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
                        modifier = Modifier.padding(horizontal = 12.dp, vertical = 5.dp)
                    )
                }
            }
        }

        LazyColumn(
            modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 6.dp, bottom = 20.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            // 1. Pinned Pending Approvals (shown in "all" and "approval" modes)
            if (category != "notice") {
                if (pending.isNotEmpty()) {
                    item {
                        Text(
                            text = "待处理审批 (${pending.size})",
                            color = p.orange,
                            fontSize = 13.sp,
                            fontWeight = FontWeight.SemiBold,
                            modifier = Modifier.padding(start = 4.dp)
                        )
                    }
                    items(pending, key = { "pending_${it.id}" }) { item ->
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
                } else if (category == "approval") {
                    item {
                        IosSection {
                            Column(
                                modifier = Modifier.fillMaxWidth().padding(vertical = 26.dp, horizontal = 20.dp),
                                horizontalAlignment = Alignment.CenterHorizontally
                            ) {
                                Icon(Icons.Default.VerifiedUser, null, tint = p.green, modifier = Modifier.size(34.dp))
                                Spacer(Modifier.height(8.dp))
                                Text("暂无待处理审批", color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
                                Spacer(Modifier.height(4.dp))
                                Text(
                                    "Claude Code / Codex / Antigravity 请求授权时会置顶弹出",
                                    color = p.secondaryLabel,
                                    fontSize = 12.sp
                                )
                            }
                        }
                    }
                }
            }

            // 2. Approval History when in "approval" filter
            if (category == "approval") {
                if (recent.isNotEmpty()) {
                    item {
                        IosSection(title = "近期审批记录") {
                            val list = recent.take(30)
                            list.forEachIndexed { idx, item ->
                                HistoryApprovalRow(item = item, onClick = { detailApproval = item })
                                if (idx < list.lastIndex) {
                                    IosDivider(startIndent = 16.dp)
                                }
                            }
                        }
                    }
                }
            } else {
                // 3. Unified Messages Feed (in "all" and "notice" modes)
                if (category == "all" && pending.isNotEmpty() && filteredMessages.isNotEmpty()) {
                    item {
                        Text(
                            text = "消息与记录",
                            color = p.secondaryLabel,
                            fontSize = 13.sp,
                            fontWeight = FontWeight.Medium,
                            modifier = Modifier.padding(start = 4.dp, top = 4.dp)
                        )
                    }
                }
                if (filteredMessages.isEmpty() && pending.isEmpty()) {
                    item {
                        IosSection {
                            Column(
                                modifier = Modifier.fillMaxWidth().padding(vertical = 32.dp, horizontal = 20.dp),
                                horizontalAlignment = Alignment.CenterHorizontally
                            ) {
                                Icon(Icons.Default.NotificationsNone, null, tint = p.tertiaryLabel, modifier = Modifier.size(34.dp))
                                Spacer(Modifier.height(8.dp))
                                Text("暂无消息", color = p.label, fontSize = 15.sp, fontWeight = FontWeight.Medium)
                                Spacer(Modifier.height(4.dp))
                                Text(
                                    "权限审批、任务完成、报错与待回答都会实时汇总在这里",
                                    color = p.secondaryLabel,
                                    fontSize = 12.sp
                                )
                            }
                        }
                    }
                } else {
                    items(filteredMessages, key = { "msg_${it.id}" }) { msg ->
                        MessageCard(
                            msg = msg,
                            onClick = {
                                if (msg.readAt == 0L) {
                                    scope.launch { AuditRepository.markMessagesRead(listOf(msg.id)) }
                                    AuditNotifier.cancelMessage(context, msg.id)
                                }
                                if (msg.tmuxSession.isNotBlank()) {
                                    AuditRepository.requestOpenTerminal("tmux_attach", msg.tmuxSession)
                                } else {
                                    detailMsg = msg
                                }
                            }
                        )
                    }
                }
            }
        }
    }

    // Deny reason dialog
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

    // Message detail dialog
    detailMsg?.let { msg ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { detailMsg = null },
            title = { Text(msg.title, fontWeight = FontWeight.SemiBold, fontSize = 16.sp) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (msg.projectName.isNotBlank()) {
                        Text("项目: ${msg.projectName}", color = p.secondaryLabel, fontSize = 12.sp)
                    }
                    if (msg.body.isNotBlank()) {
                        Text(msg.body, color = p.label, fontSize = 14.sp)
                    }
                    if (msg.taskTitle.isNotBlank() && msg.taskTitle != msg.body) {
                        Text("提示词: ${msg.taskTitle}", color = p.secondaryLabel, fontSize = 13.sp)
                    }
                    Text(formatTime(msg.createdAt), color = p.tertiaryLabel, fontSize = 11.sp)
                }
            },
            confirmButton = {
                TextButton(onClick = { detailMsg = null }) { Text("关闭", color = p.accent) }
            }
        )
    }

    // History approval detail dialog
    detailApproval?.let { item ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { detailApproval = null },
            title = { Text("审批详情", fontWeight = FontWeight.SemiBold, fontSize = 16.sp) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (item.projectName.isNotBlank()) {
                        Text("项目: ${item.projectName}", color = p.secondaryLabel, fontSize = 12.sp)
                    }
                    Text(item.summary, color = p.label, fontSize = 13.sp)
                    if (item.decisionReason.isNotBlank()) {
                        Text("备注: ${item.decisionReason}", color = p.secondaryLabel, fontSize = 12.sp)
                    }
                    Text(formatTime(item.createdAt), color = p.tertiaryLabel, fontSize = 11.sp)
                }
            },
            confirmButton = {
                TextButton(onClick = { detailApproval = null }) { Text("关闭", color = p.accent) }
            }
        )
    }
}

@Composable
private fun MessageCard(msg: AuditMessage, onClick: () -> Unit) {
    val p = LocalIosPalette.current
    val hasTerminal = msg.tmuxSession.isNotBlank()
    val accent = when (msg.eventType) {
        "error" -> p.red
        "attention" -> p.orange
        "done" -> p.green
        else -> p.accent
    }
    val previewLine = msg.body.ifBlank { msg.taskTitle }

    Surface(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(14.dp),
        color = p.card,
        border = if (p.isDark) null else BorderStroke(0.5.dp, p.separator.copy(alpha = 0.55f))
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(4.dp)) {
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
                        if (hasTerminal) {
                            Box(modifier = Modifier.size(8.dp).clip(CircleShape).background(p.green))
                        } else if (msg.readAt == 0L) {
                            Box(modifier = Modifier.size(7.dp).clip(CircleShape).background(p.accent))
                        }
                        AgentBadge(msg.agent)
                        if (msg.projectName.isNotBlank()) {
                            Text(
                                msg.projectName,
                                color = p.secondaryLabel,
                                fontSize = 12.sp,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                    }
                    Text(formatTime(msg.createdAt), fontSize = 11.sp, color = p.tertiaryLabel)
                }

                Text(
                    text = msg.title,
                    color = accent,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )

                if (previewLine.isNotBlank()) {
                    Text(
                        text = previewLine,
                        color = p.label,
                        fontSize = 13.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                }
                if (msg.taskTitle.isNotBlank() && msg.taskTitle != previewLine) {
                    Text(
                        text = msg.taskTitle,
                        color = p.secondaryLabel,
                        fontSize = 12.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                }
            }

            if (hasTerminal) {
                Icon(
                    Icons.AutoMirrored.Filled.KeyboardArrowRight,
                    contentDescription = "进入终端",
                    tint = p.green,
                    modifier = Modifier.size(20.dp)
                )
            }
        }
    }
}
