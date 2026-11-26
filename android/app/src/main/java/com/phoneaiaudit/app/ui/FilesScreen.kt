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
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.InsertDriveFile
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.AudioFile
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Code
import androidx.compose.material.icons.filled.CreateNewFolder
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.OpenInNew
import androidx.compose.material.icons.filled.PictureAsPdf
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.Slideshow
import androidx.compose.material.icons.filled.Sort
import androidx.compose.material.icons.filled.Source
import androidx.compose.material.icons.filled.TableChart
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
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
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.fragment.app.FragmentActivity
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.DownloadTaskState
import com.phoneaiaudit.app.data.FileDownloadManager
import com.phoneaiaudit.app.data.FileOpenRequest
import com.phoneaiaudit.app.data.FsEntry
import com.phoneaiaudit.app.data.FsListResult
import com.phoneaiaudit.app.data.FsSearchHit
import com.phoneaiaudit.app.data.GitRepoInfo
import com.phoneaiaudit.app.data.formatFileSize
import kotlinx.coroutines.launch

@Composable
fun FilesScreen(
    activity: FragmentActivity,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val status by AuditRepository.status.collectAsState()
    val tmuxSessions by AuditRepository.tmuxSessions.collectAsState()
    val pendingOpen by AuditRepository.pendingFileOpen.collectAsState()
    val downloadState by FileDownloadManager.state.collectAsState()

    var unlocked by remember { mutableStateOf(!AuditRepository.config.biometricForFiles) }
    var currentPath by remember { mutableStateOf("") }
    var showHidden by remember { mutableStateOf(false) }
    var sortBy by remember { mutableStateOf("name") }
    var listRes by remember { mutableStateOf<FsListResult?>(null) }
    var loading by remember { mutableStateOf(false) }
    var errorMsg by remember { mutableStateOf<String?>(null) }

    // Search state
    var searchMode by remember { mutableStateOf("name") } // "name" or "content"
    var searchQuery by remember { mutableStateOf("") }
    var searching by remember { mutableStateOf(false) }
    var searchHits by remember { mutableStateOf<List<FsSearchHit>?>(null) }

    // Full-screen overlays
    var activeFile by remember { mutableStateOf<FileOpenRequest?>(null) }
    var activeGitRepo by remember { mutableStateOf<String?>(null) }
    var quickDiffTarget by remember { mutableStateOf<Pair<String, String>?>(null) } // repoRoot to relFile

    // Menus & dialogs
    var showCreateMenu by remember { mutableStateOf(false) }
    var showSortMenu by remember { mutableStateOf(false) }
    var createDialogKind by remember { mutableStateOf<String?>(null) } // "file" or "dir"
    var createInput by remember { mutableStateOf("") }
    var renameTarget by remember { mutableStateOf<FsEntry?>(null) }
    var renameInput by remember { mutableStateOf("") }
    var trashTarget by remember { mutableStateOf<FsEntry?>(null) }

    val overlayOpen = activeFile != null || activeGitRepo != null || quickDiffTarget != null
    LaunchedEffect(overlayOpen) {
        AuditRepository.setFileOverlayActive(overlayOpen)
    }
    DisposableEffect(Unit) {
        onDispose { AuditRepository.setFileOverlayActive(false) }
    }

    fun loadDir(path: String = currentPath, toast: Boolean = false) {
        loading = true
        errorMsg = null
        scope.launch {
            val r = AuditRepository.fsList(path, showHidden, sortBy)
            loading = false
            r.onSuccess { res ->
                listRes = res
                currentPath = res.path
                if (toast) onShowSnack("刷新成功")
            }.onFailure {
                errorMsg = it.message ?: "加载目录失败"
            }
        }
    }

    LaunchedEffect(pendingOpen, unlocked) {
        val req = pendingOpen ?: return@LaunchedEffect
        if (unlocked) {
            AuditRepository.consumePendingFileOpen()
            activeFile = req
        }
    }

    LaunchedEffect(unlocked, showHidden, sortBy, status.macOnline, status.settings.filesEnabled) {
        if (unlocked && status.macOnline && status.settings.filesEnabled) {
            loadDir(currentPath)
        }
    }

    // Biometric lock gate (if enabled in Control)
    if (!unlocked) {
        Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
            IosPageHeader(title = "文件", subtitle = "已开启隐私锁")
            Column(
                modifier = Modifier.fillMaxSize().padding(32.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center
            ) {
                Icon(Icons.Default.Lock, contentDescription = null, tint = p.accent, modifier = Modifier.size(44.dp))
                Spacer(Modifier.height(12.dp))
                Text("验证身份以浏览 Mac 文件", color = p.label, fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
                Spacer(Modifier.height(16.dp))
                IosButton(text = "指纹 / 面容解锁", onClick = {
                    BiometricHelper.authenticate(
                        activity = activity,
                        title = "解锁 Mac 远程文件",
                        subtitle = "访问已授权目录与 Git 工作台",
                        onSuccess = { unlocked = true },
                        onError = { onShowSnack("验证未通过: $it") }
                    )
                })
            }
        }
        return
    }

    // Overlays
    quickDiffTarget?.let { (repoRoot, relFile) ->
        DiffViewerScreen(
            title = relFile.substringAfterLast('/'),
            repoPath = repoRoot,
            relFile = relFile,
            onDiscarded = { loadDir(currentPath) },
            onBack = { quickDiffTarget = null },
            onShowSnack = onShowSnack
        )
        return
    }
    activeGitRepo?.let { repoRoot ->
        GitWorkbenchScreen(
            activity = activity,
            repoPath = repoRoot,
            onBack = {
                activeGitRepo = null
                loadDir(currentPath)
            },
            onShowSnack = onShowSnack
        )
        return
    }
    activeFile?.let { req ->
        FileViewerScreen(
            path = req.path,
            kindHint = req.kindHint,
            jumpLine = req.jumpLine,
            onBack = {
                activeFile = null
                loadDir(currentPath)
            },
            onShowSnack = onShowSnack
        )
        return
    }

    // System back pops up parent dir if inside a subfolder
    val parentPath = listRes?.parentPath.orEmpty()
    BackHandler(enabled = searchHits != null || parentPath.isNotBlank()) {
        if (searchHits != null) {
            searchHits = null
            searchQuery = ""
        } else if (parentPath.isNotBlank()) {
            loadDir(parentPath)
        }
    }

    val configuredRoots = listRes?.roots.orEmpty()
    val activeRoot = remember(currentPath, configuredRoots) {
        configuredRoots.firstOrNull { r -> currentPath == r || currentPath.startsWith("$r/") }
            ?: configuredRoots.firstOrNull().orEmpty()
    }
    val activeRootLabel = remember(activeRoot) { formatRootLabel(activeRoot) }
    val shortSubtitle = remember(currentPath, activeRoot, activeRootLabel) {
        if (currentPath.isBlank()) "远程文件浏览与下载"
        else if (activeRoot.isNotBlank() && currentPath.startsWith(activeRoot)) {
            activeRootLabel + currentPath.removePrefix(activeRoot)
        } else currentPath
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg)) {
        IosPageHeader(
            title = "文件",
            subtitle = shortSubtitle,
            trailing = {
                Box {
                    IconButton(onClick = { showCreateMenu = true }) {
                        Icon(Icons.Default.Add, contentDescription = "新建", tint = p.accent)
                    }
                    DropdownMenu(expanded = showCreateMenu, onDismissRequest = { showCreateMenu = false }) {
                        DropdownMenuItem(
                            text = { Text("新建文本文件") },
                            leadingIcon = { Icon(Icons.AutoMirrored.Filled.InsertDriveFile, null, modifier = Modifier.size(18.dp)) },
                            onClick = { showCreateMenu = false; createInput = ""; createDialogKind = "file" }
                        )
                        DropdownMenuItem(
                            text = { Text("新建文件夹") },
                            leadingIcon = { Icon(Icons.Default.CreateNewFolder, null, modifier = Modifier.size(18.dp)) },
                            onClick = { showCreateMenu = false; createInput = ""; createDialogKind = "dir" }
                        )
                    }
                }
                IconButton(onClick = { showHidden = !showHidden }) {
                    Icon(
                        if (showHidden) Icons.Default.Visibility else Icons.Default.VisibilityOff,
                        contentDescription = "显示隐藏文件",
                        tint = if (showHidden) p.accent else p.secondaryLabel
                    )
                }
                Box {
                    IconButton(onClick = { showSortMenu = true }) {
                        Icon(Icons.Default.Sort, contentDescription = "排序", tint = p.accent)
                    }
                    DropdownMenu(expanded = showSortMenu, onDismissRequest = { showSortMenu = false }) {
                        listOf("name" to "按名称", "mtime" to "按修改时间", "size" to "按大小").forEach { (k, label) ->
                            DropdownMenuItem(
                                text = { Text(if (sortBy == k) "✓ $label" else label) },
                                onClick = { showSortMenu = false; sortBy = k }
                            )
                        }
                    }
                }
                IconButton(onClick = { loadDir(currentPath, toast = true) }) {
                    Icon(Icons.Default.Refresh, contentDescription = "刷新", tint = p.accent)
                }
            }
        )

        if (!status.macOnline || !status.settings.filesEnabled) {
            Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
                IosSection {
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(24.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                        verticalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        Text(
                            if (!status.macOnline) "Mac 守护进程离线" else "远程文件功能已关闭",
                            color = p.label,
                            fontSize = 16.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                        Text(
                            if (!status.macOnline) "请确认 Mac 已运行 ai-audit-agent" else "可在「控制」页重新开启远程文件开关",
                            color = p.secondaryLabel,
                            fontSize = 13.sp
                        )
                    }
                }
            }
            return
        }

        downloadState?.let { dl ->
            Box(modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp)) {
                DownloadStatusBanner(state = dl, onShowSnack = onShowSnack)
            }
        }

        LazyColumn(
            modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 2.dp, bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            // 1. Search bar (Filename / rg content search)
            item {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedTextField(
                            value = searchQuery,
                            onValueChange = { searchQuery = it },
                            placeholder = {
                                Text(
                                    if (searchMode == "content") "全局内容搜索 (rg)..." else "在当前目录搜索文件名...",
                                    fontSize = 13.sp
                                )
                            },
                            leadingIcon = { Icon(Icons.Default.Search, contentDescription = null, tint = p.secondaryLabel, modifier = Modifier.size(18.dp)) },
                            trailingIcon = {
                                if (searchQuery.isNotEmpty() || searchHits != null) {
                                    IconButton(onClick = { searchQuery = ""; searchHits = null }) {
                                        Icon(Icons.Default.Close, contentDescription = "清空", tint = p.secondaryLabel, modifier = Modifier.size(18.dp))
                                    }
                                }
                            },
                            singleLine = true,
                            shape = RoundedCornerShape(12.dp),
                            colors = OutlinedTextFieldDefaults.colors(
                                focusedBorderColor = p.accent,
                                unfocusedBorderColor = p.separator,
                                focusedContainerColor = p.card,
                                unfocusedContainerColor = p.card,
                                focusedTextColor = p.label,
                                unfocusedTextColor = p.label
                            ),
                            modifier = Modifier.weight(1f)
                        )
                        IosButton(
                            text = if (searching) "..." else "搜索",
                            enabled = searchQuery.isNotBlank() && !searching,
                            onClick = {
                                searching = true
                                scope.launch {
                                    AuditRepository.fsSearch(currentPath, searchQuery.trim(), searchMode)
                                        .onSuccess { searchHits = it }
                                        .onFailure { onShowSnack("搜索失败: ${it.message}") }
                                    searching = false
                                }
                            }
                        )
                    }
                    IosSegmentedControl(
                        options = listOf("name" to "按文件名", "content" to "按内容搜索 (rg)"),
                        selectedKey = searchMode,
                        onSelect = { searchMode = it }
                    )
                }
            }

            // 2. Quick Shortcuts (configured roots + active tmux project dirs inside allowed roots)
            item {
                val shortcuts = remember(configuredRoots, tmuxSessions) {
                    val list = mutableListOf<Pair<String, String>>()
                    val seen = mutableSetOf<String>()
                    configuredRoots.forEach { r ->
                        if (r.isNotBlank() && seen.add(r)) {
                            list.add(formatRootLabel(r) to r)
                        }
                    }
                    tmuxSessions.forEach { sess ->
                        val allowed = configuredRoots.isEmpty() || configuredRoots.any { r ->
                            sess.cwd == r || sess.cwd.startsWith("$r/")
                        }
                        if (sess.cwd.isNotBlank() && allowed && seen.add(sess.cwd)) {
                            list.add((sess.projectName.ifBlank { sess.cwd.substringAfterLast('/') }) to sess.cwd)
                        }
                    }
                    list
                }
                if (shortcuts.isNotEmpty()) {
                    Row(
                        modifier = Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        shortcuts.forEach { (label, target) ->
                            val active = target == currentPath
                            Surface(
                                shape = RoundedCornerShape(10.dp),
                                color = if (active) p.accent else p.card,
                                border = if (active) null else BorderStroke(0.5.dp, p.separator.copy(alpha = 0.6f)),
                                modifier = Modifier.clickable {
                                    searchHits = null
                                    loadDir(target)
                                }
                            ) {
                                Text(
                                    label,
                                    color = if (active) Color.White else p.label,
                                    fontSize = 12.sp,
                                    fontWeight = if (active) FontWeight.SemiBold else FontWeight.Medium,
                                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 7.dp)
                                )
                            }
                        }
                    }
                }
            }

            // 3. Breadcrumb bar
            if (activeRoot.isNotBlank() && currentPath.startsWith(activeRoot) && searchHits == null) {
                item {
                    val rel = currentPath.removePrefix(activeRoot).trim('/')
                    val parts = if (rel.isBlank()) emptyList() else rel.split('/')
                    Row(
                        modifier = Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        Text(
                            activeRootLabel,
                            color = if (parts.isEmpty()) p.label else p.accent,
                            fontSize = 13.sp,
                            fontWeight = FontWeight.SemiBold,
                            modifier = Modifier.clickable { loadDir(activeRoot) }.padding(vertical = 4.dp)
                        )
                        var acc = activeRoot
                        parts.forEachIndexed { idx, seg ->
                            acc = "$acc/$seg"
                            val target = acc
                            val isLast = idx == parts.lastIndex
                            Text("›", color = p.tertiaryLabel, fontSize = 13.sp)
                            Text(
                                seg,
                                color = if (isLast) p.label else p.accent,
                                fontSize = 13.sp,
                                fontWeight = if (isLast) FontWeight.SemiBold else FontWeight.Normal,
                                modifier = Modifier.clickable { if (!isLast) loadDir(target) }.padding(vertical = 4.dp)
                            )
                        }
                    }
                }
            }

            // 4. Git Repo Banner (Auto-detected!)
            val gitRepo = listRes?.gitRepo
            if (gitRepo != null && gitRepo.isRepo && searchHits == null) {
                item {
                    GitRepoBanner(
                        repo = gitRepo,
                        onOpenWorkbench = { activeGitRepo = gitRepo.root.ifBlank { currentPath } }
                    )
                }
            }

            // 5. Search Results OR Directory Entries
            if (searchHits != null) {
                val hits = searchHits!!
                item {
                    IosSection(title = "搜索结果 (${hits.size})") {
                        if (hits.isEmpty()) {
                            Box(Modifier.fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) {
                                Text("未找到匹配项", color = p.secondaryLabel, fontSize = 14.sp)
                            }
                        } else {
                            hits.forEachIndexed { idx, hit ->
                                Column(
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .clickable {
                                            if (hit.isDir) {
                                                searchHits = null
                                                loadDir(hit.path)
                                            } else {
                                                activeFile = FileOpenRequest(hit.path, jumpLine = hit.line, kindHint = hit.kind)
                                            }
                                        }
                                        .padding(horizontal = 14.dp, vertical = 11.dp),
                                    verticalArrangement = Arrangement.spacedBy(3.dp)
                                ) {
                                    Row(
                                        modifier = Modifier.fillMaxWidth(),
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.SpaceBetween
                                    ) {
                                        Row(
                                            verticalAlignment = Alignment.CenterVertically,
                                            horizontalArrangement = Arrangement.spacedBy(6.dp),
                                            modifier = Modifier.weight(1f)
                                        ) {
                                            Text(hit.name, color = p.label, fontSize = 14.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                            if (hit.line > 0) {
                                                IosPillBadge("L${hit.line}", p.accent)
                                            }
                                        }
                                        if (!hit.isDir) {
                                            IconButton(
                                                onClick = {
                                                    FileDownloadManager.startDownload(context, hit.path, hit.name)
                                                    onShowSnack("开始下载 ${hit.name}")
                                                },
                                                modifier = Modifier.size(30.dp)
                                            ) {
                                                Icon(Icons.Default.Download, contentDescription = "下载", tint = p.accent, modifier = Modifier.size(18.dp))
                                            }
                                        }
                                    }
                                    Text(hit.relPath, color = p.secondaryLabel, fontSize = 11.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    if (hit.text.isNotBlank()) {
                                        Text(
                                            hit.text,
                                            color = p.codeFg,
                                            fontFamily = FontFamily.Monospace,
                                            fontSize = 11.5.sp,
                                            maxLines = 2,
                                            overflow = TextOverflow.Ellipsis
                                        )
                                    }
                                }
                                if (idx < hits.lastIndex) IosDivider()
                            }
                        }
                    }
                }
            } else {
                item {
                    when {
                        loading && listRes == null -> Box(Modifier.fillMaxWidth().padding(32.dp), contentAlignment = Alignment.Center) {
                            CircularProgressIndicator(color = p.accent, modifier = Modifier.size(26.dp))
                        }
                        errorMsg != null -> IosSection {
                            Text(errorMsg!!, color = p.red, fontSize = 13.sp, modifier = Modifier.padding(16.dp))
                        }
                        listRes != null -> {
                            val entries = listRes!!.entries
                            IosSection {
                                if (parentPath.isNotBlank()) {
                                    Row(
                                        modifier = Modifier
                                            .fillMaxWidth()
                                            .clickable { loadDir(parentPath) }
                                            .padding(horizontal = 14.dp, vertical = 12.dp),
                                        verticalAlignment = Alignment.CenterVertically
                                    ) {
                                        Icon(Icons.Default.Folder, contentDescription = null, tint = p.accent, modifier = Modifier.size(22.dp))
                                        Spacer(Modifier.width(12.dp))
                                        Text("..（返回上一级）", color = p.accent, fontSize = 14.sp, fontWeight = FontWeight.Medium)
                                    }
                                    if (entries.isNotEmpty()) IosDivider(startIndent = 48.dp)
                                }
                                if (entries.isEmpty()) {
                                    Box(Modifier.fillMaxWidth().padding(28.dp), contentAlignment = Alignment.Center) {
                                        Text("空目录", color = p.secondaryLabel, fontSize = 14.sp)
                                    }
                                } else {
                                    entries.forEachIndexed { idx, entry ->
                                        FileEntryRow(
                                            entry = entry,
                                            inGitRepo = gitRepo?.isRepo == true,
                                            onClick = {
                                                if (entry.isDir) {
                                                    loadDir(entry.path)
                                                } else {
                                                    activeFile = FileOpenRequest(entry.path, kindHint = entry.kind)
                                                }
                                            },
                                            onDownload = {
                                                FileDownloadManager.startDownload(context, entry.path, entry.name)
                                                onShowSnack("开始下载 ${entry.name}")
                                            },
                                            onViewDiff = {
                                                val root = gitRepo?.root.orEmpty()
                                                if (root.isNotBlank() && entry.path.startsWith("$root/")) {
                                                    quickDiffTarget = root to entry.path.removePrefix("$root/")
                                                }
                                            },
                                            onRename = {
                                                renameTarget = entry
                                                renameInput = entry.name
                                            },
                                            onTrash = { trashTarget = entry }
                                        )
                                        if (idx < entries.lastIndex) IosDivider(startIndent = 48.dp)
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    // Create File / Folder Dialog
    createDialogKind?.let { kind ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { createDialogKind = null },
            title = { Text(if (kind == "dir") "新建文件夹" else "新建文本文件", fontWeight = FontWeight.SemiBold) },
            text = {
                OutlinedTextField(
                    value = createInput,
                    onValueChange = { createInput = it },
                    label = { Text(if (kind == "dir") "文件夹名称" else "文件名（如 notes.md）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                IosButton(text = "创建", enabled = createInput.isNotBlank(), onClick = {
                    val name = createInput.trim()
                    val full = "$currentPath/$name"
                    createDialogKind = null
                    scope.launch {
                        val r = if (kind == "dir") AuditRepository.fsMkdir(full) else AuditRepository.fsTouch(full)
                        r.onSuccess {
                            onShowSnack("已创建 $name")
                            loadDir(currentPath)
                        }.onFailure { onShowSnack("创建失败: ${it.message}") }
                    }
                })
            },
            dismissButton = { TextButton(onClick = { createDialogKind = null }) { Text("取消", color = p.secondaryLabel) } }
        )
    }

    // Rename Dialog
    renameTarget?.let { entry ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { renameTarget = null },
            title = { Text("重命名", fontWeight = FontWeight.SemiBold) },
            text = {
                OutlinedTextField(
                    value = renameInput,
                    onValueChange = { renameInput = it },
                    label = { Text("新名称") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                IosButton(text = "确定", enabled = renameInput.isNotBlank() && renameInput.trim() != entry.name, onClick = {
                    val newName = renameInput.trim()
                    renameTarget = null
                    scope.launch {
                        AuditRepository.fsRename(entry.path, newName)
                            .onSuccess {
                                onShowSnack("已重命名为 $newName")
                                loadDir(currentPath)
                            }
                            .onFailure { onShowSnack("重命名失败: ${it.message}") }
                    }
                })
            },
            dismissButton = { TextButton(onClick = { renameTarget = null }) { Text("取消", color = p.secondaryLabel) } }
        )
    }

    // Move to Trash Dialog
    trashTarget?.let { entry ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { trashTarget = null },
            title = { Text("移至 Mac 废纸篓？", fontWeight = FontWeight.SemiBold) },
            text = {
                Text(
                    "「${entry.name}」将被移动到 Mac 的 ~/.Trash 废纸篓中（可从 Finder 恢复，不会直接永久删除）。",
                    color = p.secondaryLabel,
                    fontSize = 13.sp
                )
            },
            confirmButton = {
                IosButton(text = "移至废纸篓", containerColor = p.red, onClick = {
                    val target = entry
                    trashTarget = null
                    scope.launch {
                        AuditRepository.fsTrash(target.path)
                            .onSuccess {
                                onShowSnack("已移至 Mac 废纸篓")
                                loadDir(currentPath)
                            }
                            .onFailure { onShowSnack("删除失败: ${it.message}") }
                    }
                })
            },
            dismissButton = { TextButton(onClick = { trashTarget = null }) { Text("取消", color = p.secondaryLabel) } }
        )
    }
}

@Composable
private fun GitRepoBanner(repo: GitRepoInfo, onOpenWorkbench: () -> Unit) {
    val p = LocalIosPalette.current
    Surface(
        modifier = Modifier.fillMaxWidth().clickable { onOpenWorkbench() },
        shape = RoundedCornerShape(14.dp),
        color = p.accent.copy(alpha = if (p.isDark) 0.16f else 0.10f),
        border = BorderStroke(0.5.dp, p.accent.copy(alpha = 0.4f))
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(10.dp),
                modifier = Modifier.weight(1f)
            ) {
                Icon(Icons.Default.Source, contentDescription = null, tint = p.accent, modifier = Modifier.size(22.dp))
                Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Text(repo.branch.ifBlank { "git" }, color = p.label, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                        if (repo.ahead > 0) IosPillBadge("↑${repo.ahead}", p.green)
                        if (repo.behind > 0) IosPillBadge("↓${repo.behind}", p.orange)
                    }
                    Text(
                        text = if (repo.dirtyCount > 0) "${repo.dirtyCount} 个文件有未提交改动 · 点击查看 Diff / Commit / Push" else "工作区干净 · 点击进入 Git 工作台",
                        color = p.secondaryLabel,
                        fontSize = 11.5.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                }
            }
            Text("工作台 ›", color = p.accent, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
        }
    }
}

@Composable
internal fun DownloadStatusBanner(
    state: DownloadTaskState,
    onShowSnack: (String) -> Unit,
    darkSurface: Boolean = false
) {
    val p = LocalIosPalette.current
    val context = LocalContext.current
    val bgColor = if (darkSurface) Color(0xFF1C1C1E) else p.card
    val titleColor = if (darkSurface) Color.White else p.label
    val subColor = if (darkSurface) Color(0xFFAEAEB2) else p.secondaryLabel
    val borderColor = when {
        state.error != null -> p.red.copy(alpha = 0.5f)
        state.finished != null -> p.green.copy(alpha = 0.55f)
        else -> p.accent.copy(alpha = 0.45f)
    }

    Surface(
        modifier = Modifier.fillMaxWidth(),
        shape = RoundedCornerShape(14.dp),
        color = bgColor,
        border = BorderStroke(0.8.dp, borderColor)
    ) {
        Column(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Icon(
                    imageVector = when {
                        state.finished != null -> Icons.Default.CheckCircle
                        state.error != null -> Icons.Default.Close
                        else -> Icons.Default.Download
                    },
                    contentDescription = null,
                    tint = when {
                        state.finished != null -> p.green
                        state.error != null -> p.red
                        else -> p.accent
                    },
                    modifier = Modifier.size(18.dp)
                )
                Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    Text(
                        text = when {
                            state.finished != null -> "下载完成 · ${state.fileName}"
                            state.error != null -> "下载失败 · ${state.fileName}"
                            else -> "正在下载 · ${state.fileName}"
                        },
                        color = titleColor,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                    val detail = when {
                        state.finished != null -> "已保存至 ${state.finished.relativePath} (${formatFileSize(state.finished.sizeBytes)})"
                        state.error != null -> state.error
                        state.totalBytes > 0L -> {
                            val pct = ((state.bytesRead * 100L) / state.totalBytes).coerceIn(0L, 100L)
                            "${formatFileSize(state.bytesRead)} / ${formatFileSize(state.totalBytes)} · $pct%"
                        }
                        else -> "已接收 ${formatFileSize(state.bytesRead)}..."
                    }
                    Text(detail, color = if (state.error != null) p.red else subColor, fontSize = 11.5.sp, maxLines = 2, overflow = TextOverflow.Ellipsis)
                }
                if (state.isDownloading) {
                    TextButton(onClick = { FileDownloadManager.cancelDownload() }) {
                        Text("取消", color = p.red, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
                    }
                } else {
                    IconButton(onClick = { FileDownloadManager.dismiss() }, modifier = Modifier.size(28.dp)) {
                        Icon(Icons.Default.Close, contentDescription = "关闭", tint = subColor, modifier = Modifier.size(16.dp))
                    }
                }
            }

            if (state.isDownloading) {
                val prog = state.progress
                if (prog != null) {
                    LinearProgressIndicator(
                        progress = { prog },
                        modifier = Modifier.fillMaxWidth().height(4.dp).clip(RoundedCornerShape(2.dp)),
                        color = p.accent,
                        trackColor = p.secondaryFill
                    )
                } else {
                    LinearProgressIndicator(
                        modifier = Modifier.fillMaxWidth().height(4.dp).clip(RoundedCornerShape(2.dp)),
                        color = p.accent,
                        trackColor = p.secondaryFill
                    )
                }
            } else if (state.finished != null) {
                Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    IosTintedButton(
                        text = "打开文件",
                        icon = Icons.Default.OpenInNew,
                        tint = p.accent,
                        onClick = {
                            FileDownloadManager.openDownloadedFile(context, state.finished)
                                .onFailure { onShowSnack("未找到可打开该文件的应用，可到系统「下载/PhoneAiAudit」目录查看") }
                        },
                        modifier = Modifier.weight(1f)
                    )
                    IosTintedButton(
                        text = "分享",
                        icon = Icons.Default.Share,
                        tint = p.green,
                        onClick = {
                            FileDownloadManager.shareDownloadedFile(context, state.finished)
                                .onFailure { onShowSnack("分享失败: ${it.message}") }
                        },
                        modifier = Modifier.weight(1f)
                    )
                }
            } else if (state.error != null) {
                IosTintedButton(
                    text = "重新下载",
                    icon = Icons.Default.Download,
                    tint = p.accent,
                    onClick = { FileDownloadManager.startDownload(context, state.remotePath, state.fileName) },
                    modifier = Modifier.fillMaxWidth()
                )
            }
        }
    }
}

@Composable
private fun FileEntryRow(
    entry: FsEntry,
    inGitRepo: Boolean,
    onClick: () -> Unit,
    onDownload: () -> Unit,
    onViewDiff: () -> Unit,
    onRename: () -> Unit,
    onTrash: () -> Unit
) {
    val p = LocalIosPalette.current
    var menuOpen by remember { mutableStateOf(false) }
    val (icon, tint) = fileIconAndTint(entry.kind, p)

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onClick() }
            .padding(start = 14.dp, end = 4.dp, top = 11.dp, bottom = 11.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Icon(icon, contentDescription = null, tint = tint, modifier = Modifier.size(22.dp))
        Spacer(Modifier.width(12.dp))
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(
                    text = entry.name,
                    color = p.label,
                    fontSize = 14.5.sp,
                    fontWeight = if (entry.isDir) FontWeight.Medium else FontWeight.Normal,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false)
                )
                if (entry.isSensitive) {
                    Icon(Icons.Default.Shield, contentDescription = "敏感文件", tint = p.orange, modifier = Modifier.size(13.dp))
                }
                if (entry.gitStatus.isNotBlank()) {
                    IosPillBadge(entry.gitStatus, gitStatusColor(entry.gitStatus, p))
                }
            }
            val meta = if (entry.isDir) {
                formatTime(entry.modTime)
            } else {
                "${formatFileSize(entry.size)} · ${formatTime(entry.modTime)}"
            }
            Text(meta, color = p.secondaryLabel, fontSize = 11.5.sp, maxLines = 1)
        }
        Box {
            IconButton(onClick = { menuOpen = true }, modifier = Modifier.size(36.dp)) {
                Icon(Icons.Default.MoreVert, contentDescription = "更多", tint = p.tertiaryLabel, modifier = Modifier.size(18.dp))
            }
            DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                if (!entry.isDir) {
                    DropdownMenuItem(
                        text = { Text("下载到手机", color = p.accent, fontWeight = FontWeight.Medium) },
                        leadingIcon = { Icon(Icons.Default.Download, null, tint = p.accent, modifier = Modifier.size(18.dp)) },
                        onClick = { menuOpen = false; onDownload() }
                    )
                }
                if (inGitRepo && !entry.isDir && entry.gitStatus.isNotBlank()) {
                    DropdownMenuItem(
                        text = { Text("查看 Git Diff") },
                        onClick = { menuOpen = false; onViewDiff() }
                    )
                }
                DropdownMenuItem(
                    text = { Text("重命名") },
                    onClick = { menuOpen = false; onRename() }
                )
                DropdownMenuItem(
                    text = { Text("移至 Mac 废纸篓", color = p.red) },
                    onClick = { menuOpen = false; onTrash() }
                )
            }
        }
    }
}

private fun formatRootLabel(root: String): String {
    val clean = root.trimEnd('/')
    if (clean.isBlank()) return "~"
    val macPrefix = Regex("^/Users/[^/]+")
    val linuxPrefix = Regex("^/home/[^/]+")
    return when {
        macPrefix.containsMatchIn(clean) -> clean.replaceFirst(macPrefix, "~")
        linuxPrefix.containsMatchIn(clean) -> clean.replaceFirst(linuxPrefix, "~")
        else -> clean.substringAfterLast('/').ifBlank { clean }
    }
}

private fun fileIconAndTint(kind: String, p: IosPalette): Pair<ImageVector, Color> = when (kind) {
    "dir" -> Icons.Default.Folder to p.accent
    "markdown" -> Icons.Default.Description to Color(0xFF5E5CE6)
    "code" -> Icons.Default.Code to p.green
    "image" -> Icons.Default.Image to p.orange
    "pdf" -> Icons.Default.PictureAsPdf to p.red
    "excel" -> Icons.Default.TableChart to Color(0xFF28CD41)
    "word" -> Icons.Default.Description to p.accent
    "pptx" -> Icons.Default.Slideshow to Color(0xFFFF6961)
    "video" -> Icons.Default.Movie to Color(0xFFBF5AF2)
    "audio" -> Icons.Default.AudioFile to Color(0xFFFF2D55)
    else -> Icons.AutoMirrored.Filled.InsertDriveFile to p.secondaryLabel
}
