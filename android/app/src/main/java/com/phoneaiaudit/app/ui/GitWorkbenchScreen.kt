package com.phoneaiaudit.app.ui

import androidx.activity.compose.BackHandler
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
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.CloudDownload
import androidx.compose.material.icons.filled.CloudUpload
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Restore
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.fragment.app.FragmentActivity
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.GitChangedFile
import com.phoneaiaudit.app.data.GitLogEntry
import com.phoneaiaudit.app.data.GitStatusResult
import kotlinx.coroutines.launch

@Composable
fun GitWorkbenchScreen(
    activity: FragmentActivity,
    repoPath: String,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val scope = rememberCoroutineScope()

    var statusRes by remember { mutableStateOf<GitStatusResult?>(null) }
    var loading by remember { mutableStateOf(true) }
    var busyAction by remember { mutableStateOf(false) }
    var errorMsg by remember { mutableStateOf<String?>(null) }

    var selectedPaths by remember { mutableStateOf<Set<String>>(emptySet()) }
    var commitMsg by remember { mutableStateOf("") }

    var diffFile by remember { mutableStateOf<GitChangedFile?>(null) }
    var diffCommit by remember { mutableStateOf<GitLogEntry?>(null) }
    var discardTarget by remember { mutableStateOf<GitChangedFile?>(null) }
    var showPushConfirm by remember { mutableStateOf(false) }
    var showLogDialog by remember { mutableStateOf(false) }
    var logEntries by remember { mutableStateOf<List<GitLogEntry>>(emptyList()) }
    var loadingLog by remember { mutableStateOf(false) }

    fun refreshStatus(toast: Boolean = false) {
        loading = true
        errorMsg = null
        scope.launch {
            val r = AuditRepository.gitStatus(repoPath)
            loading = false
            r.onSuccess { res ->
                val prevFiles = statusRes?.files?.map { it.path }?.toSet().orEmpty()
                val newFiles = res.files.map { it.path }.toSet()
                statusRes = res
                selectedPaths = if (prevFiles.isEmpty()) {
                    newFiles
                } else {
                    (selectedPaths intersect newFiles) + (newFiles - prevFiles)
                }
                if (toast) onShowSnack("Git 状态已刷新")
            }.onFailure {
                errorMsg = it.message ?: "读取 Git 状态失败"
            }
        }
    }

    LaunchedEffect(repoPath) {
        refreshStatus()
    }

    // Sub-overlay: file Diff or commit Diff
    diffFile?.let { cf ->
        DiffViewerScreen(
            title = cf.path.substringAfterLast('/'),
            repoPath = statusRes?.repo?.root?.ifBlank { repoPath } ?: repoPath,
            relFile = cf.path,
            initialStaged = cf.staged && cf.worktreeStatus == " ",
            onDiscarded = { refreshStatus() },
            onBack = { diffFile = null },
            onShowSnack = onShowSnack
        )
        return
    }
    diffCommit?.let { entry ->
        DiffViewerScreen(
            title = "${entry.shortSha} · ${entry.subject}",
            repoPath = statusRes?.repo?.root?.ifBlank { repoPath } ?: repoPath,
            commitSha = entry.sha,
            onBack = { diffCommit = null },
            onShowSnack = onShowSnack
        )
        return
    }

    BackHandler { onBack() }

    val repo = statusRes?.repo
    val files = statusRes?.files.orEmpty()
    val allSelected = files.isNotEmpty() && selectedPaths.size == files.size

    fun executePush() {
        val targetRoot = repo?.root?.ifBlank { repoPath } ?: repoPath
        busyAction = true
        scope.launch {
            val res = AuditRepository.gitPush(targetRoot)
            busyAction = false
            res.onSuccess { pr ->
                onShowSnack("推送成功 → ${pr.remote}/${pr.branch}")
                refreshStatus()
            }.onFailure {
                onShowSnack("推送失败: ${it.message}")
            }
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(p.bg)
            .imePadding()
            .navigationBarsPadding()
    ) {
        // Header bar
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(p.card)
                .statusBarsPadding()
                .padding(horizontal = 6.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            IconButton(onClick = onBack) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回", tint = p.accent)
            }
            Column(modifier = Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        text = repo?.repoName?.ifBlank { "Git 工作台" } ?: "Git 工作台",
                        color = p.label,
                        fontSize = 16.sp,
                        fontWeight = FontWeight.Bold,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                    if (repo != null && repo.branch.isNotBlank()) {
                        IosPillBadge(
                            text = repo.branch,
                            color = if (repo.protected) p.orange else p.accent
                        )
                    }
                }
                if (repo != null) {
                    val syncTxt = buildString {
                        if (repo.upstream.isNotBlank()) append(repo.upstream) else append(repo.remote.ifBlank { "local" })
                        append("  ↑${repo.ahead}  ↓${repo.behind}")
                    }
                    Text(syncTxt, color = p.secondaryLabel, fontSize = 11.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
            IconButton(
                enabled = !busyAction,
                onClick = {
                    busyAction = true
                    scope.launch {
                        AuditRepository.gitPull(repo?.root?.ifBlank { repoPath } ?: repoPath)
                            .onSuccess {
                                onShowSnack(it.ifBlank { "拉取完成" })
                                refreshStatus()
                            }
                            .onFailure { onShowSnack("拉取失败: ${it.message}") }
                        busyAction = false
                    }
                }
            ) {
                Icon(Icons.Default.CloudDownload, contentDescription = "拉取", tint = p.accent)
            }
            IconButton(onClick = {
                showLogDialog = true
                loadingLog = true
                scope.launch {
                    AuditRepository.gitLog(repo?.root?.ifBlank { repoPath } ?: repoPath, 20)
                        .onSuccess { logEntries = it }
                        .onFailure { onShowSnack("读取历史失败: ${it.message}") }
                    loadingLog = false
                }
            }) {
                Icon(Icons.Default.History, contentDescription = "提交历史", tint = p.accent)
            }
            IconButton(onClick = { refreshStatus(toast = true) }) {
                Icon(Icons.Default.Refresh, contentDescription = "刷新", tint = p.accent)
            }
        }
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)

        // File list
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading && statusRes == null -> {
                    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                        CircularProgressIndicator(color = p.accent, modifier = Modifier.size(28.dp))
                    }
                }
                errorMsg != null && statusRes == null -> {
                    Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
                        Text(errorMsg!!, color = p.red, fontSize = 14.sp)
                    }
                }
                files.isEmpty() -> {
                    Column(
                        modifier = Modifier.fillMaxSize().padding(24.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                        verticalArrangement = Arrangement.Center
                    ) {
                        Icon(Icons.Default.CheckCircle, contentDescription = null, tint = p.green, modifier = Modifier.size(42.dp))
                        Spacer(Modifier.height(10.dp))
                        Text("工作区干净", color = p.label, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
                        Spacer(Modifier.height(4.dp))
                        Text(
                            text = if ((repo?.ahead ?: 0) > 0) "有 ${repo?.ahead} 个本地提交等待推送到远端" else "所有改动均已提交",
                            color = p.secondaryLabel,
                            fontSize = 13.sp
                        )
                        Spacer(Modifier.height(16.dp))
                        IosTintedButton(
                            text = "打开终端处理分支/合并",
                            icon = Icons.Default.Terminal,
                            onClick = { AuditRepository.selectTab(1); onBack() }
                        )
                    }
                }
                else -> {
                    LazyColumn(
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(16.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp)
                    ) {
                        item {
                            Row(
                                modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp),
                                horizontalArrangement = Arrangement.SpaceBetween,
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                Text(
                                    text = "变更文件 (${selectedPaths.size}/${files.size} 已选)",
                                    color = p.secondaryLabel,
                                    fontSize = 13.sp,
                                    fontWeight = FontWeight.Medium
                                )
                                Text(
                                    text = if (allSelected) "取消全选" else "全选",
                                    color = p.accent,
                                    fontSize = 13.sp,
                                    fontWeight = FontWeight.SemiBold,
                                    modifier = Modifier.clickable {
                                        selectedPaths = if (allSelected) emptySet() else files.map { it.path }.toSet()
                                    }
                                )
                            }
                        }

                        item {
                            IosSection {
                                files.forEachIndexed { idx, cf ->
                                    val checked = cf.path in selectedPaths
                                    GitChangedFileRow(
                                        file = cf,
                                        checked = checked,
                                        onToggle = {
                                            selectedPaths = if (checked) selectedPaths - cf.path else selectedPaths + cf.path
                                        },
                                        onOpenDiff = { diffFile = cf },
                                        onDiscard = { discardTarget = cf }
                                    )
                                    if (idx < files.lastIndex) {
                                        IosDivider(startIndent = 48.dp)
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        // Bottom Commit & Push Bar
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .background(p.card)
                .padding(horizontal = 14.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            if (files.isNotEmpty()) {
                val prefixes = listOf("feat: ", "fix: ", "refactor: ", "docs: ", "chore: ")
                Row(
                    modifier = Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                    horizontalArrangement = Arrangement.spacedBy(6.dp)
                ) {
                    prefixes.forEach { pre ->
                        AssistChip(
                            onClick = {
                                if (!commitMsg.startsWith(pre)) {
                                    commitMsg = pre + commitMsg.replace(Regex("^(feat|fix|refactor|docs|chore):\\s*"), "")
                                }
                            },
                            label = { Text(pre.trim(), fontSize = 11.sp, color = p.accent) }
                        )
                    }
                }
                OutlinedTextField(
                    value = commitMsg,
                    onValueChange = { commitMsg = it },
                    placeholder = { Text("输入 Commit 说明（仅提交勾选的 ${selectedPaths.size} 个文件）", fontSize = 13.sp) },
                    singleLine = false,
                    maxLines = 3,
                    shape = RoundedCornerShape(10.dp),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = p.accent,
                        unfocusedBorderColor = p.separator,
                        focusedTextColor = p.label,
                        unfocusedTextColor = p.label
                    ),
                    modifier = Modifier.fillMaxWidth()
                )
            }

            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                if (files.isNotEmpty()) {
                    IosButton(
                        text = if (busyAction) "提交中..." else "提交所选 (${selectedPaths.size})",
                        icon = Icons.Default.Check,
                        containerColor = p.accent,
                        enabled = !busyAction && selectedPaths.isNotEmpty() && commitMsg.isNotBlank(),
                        onClick = {
                            val targetRoot = repo?.root?.ifBlank { repoPath } ?: repoPath
                            val chosen = files.map { it.path }.filter { it in selectedPaths }
                            busyAction = true
                            scope.launch {
                                val res = AuditRepository.gitCommit(targetRoot, chosen, commitMsg.trim())
                                busyAction = false
                                res.onSuccess { cr ->
                                    commitMsg = ""
                                    onShowSnack("已提交 ${cr.shortSha} (${chosen.size} 个文件)")
                                    refreshStatus()
                                }.onFailure {
                                    onShowSnack("提交失败: ${it.message}")
                                }
                            }
                        },
                        modifier = Modifier.weight(1.25f)
                    )
                }
                val ahead = repo?.ahead ?: 0
                val pushText = when {
                    busyAction -> "处理中..."
                    ahead > 0 -> "推送 ↑$ahead"
                    else -> "推送 Push"
                }
                IosButton(
                    text = pushText,
                    icon = Icons.Default.CloudUpload,
                    containerColor = if (repo?.protected == true) p.orange else p.green,
                    enabled = !busyAction && repo != null && (ahead > 0 || repo.upstream.isBlank()),
                    onClick = { showPushConfirm = true },
                    modifier = Modifier.weight(1f)
                )
            }
        }
    }

    // Push confirmation dialog (strictly no --force; protected branches require biometric!)
    if (showPushConfirm && repo != null) {
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { showPushConfirm = false },
            title = {
                Text(
                    if (repo.protected) "确认推送到受保护分支？" else "确认推送到远端？",
                    fontWeight = FontWeight.SemiBold
                )
            },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text("仓库：${repo.repoName}", color = p.label, fontSize = 13.sp)
                    Text("分支：${repo.branch} → ${repo.upstream.ifBlank { "${repo.remote.ifBlank { "origin" }}/${repo.branch}" }}", color = p.secondaryLabel, fontSize = 13.sp)
                    Text("待推送提交：${repo.ahead} 个（严格使用安全 push，绝不 force）", color = p.secondaryLabel, fontSize = 12.sp)
                    if (repo.protected) {
                        Text(
                            "注意：${repo.branch} 属于受保护主干/发布分支，需要指纹/面容确认。",
                            color = p.orange,
                            fontSize = 12.sp,
                            fontWeight = FontWeight.Medium
                        )
                    }
                }
            },
            confirmButton = {
                IosButton(
                    text = if (repo.protected) "验证并推送" else "确认推送",
                    containerColor = if (repo.protected) p.orange else p.green,
                    onClick = {
                        showPushConfirm = false
                        if (repo.protected) {
                            BiometricHelper.authenticate(
                                activity = activity,
                                title = "确认推送到受保护分支",
                                subtitle = "${repo.repoName} · ${repo.branch}",
                                onSuccess = { executePush() },
                                onError = { onShowSnack("已取消推送验证: $it") }
                            )
                        } else {
                            executePush()
                        }
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { showPushConfirm = false }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }

    // Discard confirmation dialog
    discardTarget?.let { cf ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { discardTarget = null },
            title = { Text("撤销单个文件改动？", fontWeight = FontWeight.SemiBold) },
            text = {
                Text(
                    "将撤销 ${cf.path} 的改动（若为未跟踪文件将移至 Mac 废纸篓）。",
                    color = p.secondaryLabel,
                    fontSize = 13.sp
                )
            },
            confirmButton = {
                IosButton(
                    text = "撤销",
                    containerColor = p.red,
                    onClick = {
                        val f = cf
                        discardTarget = null
                        scope.launch {
                            AuditRepository.gitDiscard(repo?.root?.ifBlank { repoPath } ?: repoPath, f.path)
                                .onSuccess {
                                    onShowSnack("已撤销 ${f.path}")
                                    refreshStatus()
                                }
                                .onFailure { onShowSnack("撤销失败: ${it.message}") }
                        }
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { discardTarget = null }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }

    // Commit Log Dialog
    if (showLogDialog) {
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { showLogDialog = false },
            title = { Text("最近提交记录（点击看 Diff）", fontWeight = FontWeight.SemiBold, fontSize = 16.sp) },
            text = {
                if (loadingLog) {
                    Box(Modifier.fillMaxWidth().height(120.dp), contentAlignment = Alignment.Center) {
                        CircularProgressIndicator(color = p.accent, modifier = Modifier.size(24.dp))
                    }
                } else if (logEntries.isEmpty()) {
                    Text("暂无提交记录", color = p.secondaryLabel, fontSize = 13.sp)
                } else {
                    LazyColumn(modifier = Modifier.fillMaxWidth().height(320.dp)) {
                        itemsIndexed(logEntries) { idx, e ->
                            Column(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clickable {
                                        showLogDialog = false
                                        diffCommit = e
                                    }
                                    .padding(vertical = 8.dp),
                                verticalArrangement = Arrangement.spacedBy(2.dp)
                            ) {
                                Row(
                                    modifier = Modifier.fillMaxWidth(),
                                    horizontalArrangement = Arrangement.SpaceBetween,
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Text(e.shortSha, color = p.accent, fontFamily = FontFamily.Monospace, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                                    Text("${e.author} · ${e.date}", color = p.tertiaryLabel, fontSize = 11.sp)
                                }
                                Text(e.subject, color = p.label, fontSize = 13.sp, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            }
                            if (idx < logEntries.lastIndex) {
                                HorizontalDivider(thickness = 0.5.dp, color = p.separator.copy(alpha = 0.6f))
                            }
                        }
                    }
                }
            },
            confirmButton = {
                TextButton(onClick = { showLogDialog = false }) { Text("关闭", color = p.accent) }
            }
        )
    }
}

@Composable
private fun GitChangedFileRow(
    file: GitChangedFile,
    checked: Boolean,
    onToggle: () -> Unit,
    onOpenDiff: () -> Unit,
    onDiscard: () -> Unit
) {
    val p = LocalIosPalette.current
    val badgeColor = gitStatusColor(file.statusCode, p)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onOpenDiff() }
            .padding(start = 12.dp, end = 6.dp, top = 10.dp, bottom = 10.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        // iOS circular checkbox
        Surface(
            modifier = Modifier
                .size(24.dp)
                .clip(CircleShape)
                .clickable { onToggle() },
            shape = CircleShape,
            color = if (checked) p.accent else Color.Transparent,
            border = if (checked) null else BorderStroke(1.5.dp, p.tertiaryLabel)
        ) {
            if (checked) {
                Box(contentAlignment = Alignment.Center) {
                    Icon(Icons.Default.Check, contentDescription = null, tint = Color.White, modifier = Modifier.size(15.dp))
                }
            }
        }
        Spacer(Modifier.width(12.dp))
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(
                    text = file.path.substringAfterLast('/'),
                    color = p.label,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false)
                )
                IosPillBadge(text = file.statusCode, color = badgeColor)
                if (file.staged) {
                    IosPillBadge(text = "S", color = p.green)
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                val dirPart = file.path.substringBeforeLast('/', "")
                if (dirPart.isNotBlank()) {
                    Text(
                        text = dirPart,
                        color = p.secondaryLabel,
                        fontSize = 11.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false)
                    )
                }
                if (file.added > 0) Text("+${file.added}", color = p.green, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
                if (file.deleted > 0) Text("-${file.deleted}", color = p.red, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
            }
        }
        IconButton(onClick = onDiscard, modifier = Modifier.size(36.dp)) {
            Icon(Icons.Default.Restore, contentDescription = "撤销", tint = p.tertiaryLabel, modifier = Modifier.size(18.dp))
        }
    }
}

fun gitStatusColor(code: String, p: IosPalette): Color = when (code) {
    "M" -> p.orange
    "A" -> p.green
    "?" -> p.accent
    "D", "U" -> p.red
    "R" -> Color(0xFFAF52DE)
    else -> p.secondaryLabel
}
