package com.phoneaiaudit.app.ui

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.view.ViewGroup
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
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
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowLeft
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.automirrored.filled.WrapText
import androidx.compose.material.icons.filled.CompareArrows
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Hd
import androidx.compose.material.icons.filled.InsertDriveFile
import androidx.compose.material.icons.filled.Save
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material3.AlertDialog
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
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.media3.common.MediaItem
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.ui.PlayerView
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.ExcelMeta
import com.phoneaiaudit.app.data.ExcelSheetData
import com.phoneaiaudit.app.data.FileDownloadManager
import com.phoneaiaudit.app.data.FsConflictException
import com.phoneaiaudit.app.data.FsReadResult
import com.phoneaiaudit.app.data.PdfMeta
import com.phoneaiaudit.app.data.PptxOutline
import com.phoneaiaudit.app.data.formatFileSize
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicReference

@Composable
fun FileViewerScreen(
    path: String,
    kindHint: String = "",
    jumpLine: Int = 0,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val context = LocalContext.current
    val fileName = remember(path) { path.substringAfterLast('/') }
    val downloadState by FileDownloadManager.state.collectAsState()
    var effectiveKind by remember(path, kindHint) {
        mutableStateOf(kindHint.ifBlank { inferKindFromName(fileName) })
    }
    val onDownload: () -> Unit = {
        FileDownloadManager.startDownload(context, path, fileName)
        onShowSnack("开始下载 $fileName")
    }
    val isDarkViewer = effectiveKind in setOf("image", "video", "audio")

    Box(modifier = Modifier.fillMaxSize()) {
        when (effectiveKind) {
            "code", "markdown" -> TextMarkdownViewer(
                path = path,
                fileName = fileName,
                isMarkdown = effectiveKind == "markdown",
                jumpLine = jumpLine,
                onDownload = onDownload,
                onBack = onBack,
                onShowSnack = onShowSnack
            )
            "image" -> ImageStreamViewer(path = path, fileName = fileName, onDownload = onDownload, onBack = onBack, onShowSnack = onShowSnack)
            "pdf" -> PdfStreamViewer(path = path, fileName = fileName, onDownload = onDownload, onBack = onBack, onShowSnack = onShowSnack)
            "excel" -> ExcelViewer(path = path, fileName = fileName, onDownload = onDownload, onBack = onBack, onShowSnack = onShowSnack)
            "word" -> WordHtmlViewer(path = path, fileName = fileName, onDownload = onDownload, onBack = onBack)
            "pptx" -> PptxOutlineViewer(path = path, fileName = fileName, onDownload = onDownload, onBack = onBack)
            "video", "audio" -> MediaStreamViewer(path = path, fileName = fileName, isAudio = effectiveKind == "audio", onDownload = onDownload, onBack = onBack)
            else -> BinaryFallbackViewer(
                path = path,
                fileName = fileName,
                onDownload = onDownload,
                onTryText = { effectiveKind = "code" },
                onBack = onBack
            )
        }

        downloadState?.let { dl ->
            Box(
                modifier = Modifier
                    .align(Alignment.BottomCenter)
                    .fillMaxWidth()
                    .navigationBarsPadding()
                    .padding(horizontal = 14.dp, vertical = 12.dp)
            ) {
                DownloadStatusBanner(state = dl, onShowSnack = onShowSnack, darkSurface = isDarkViewer)
            }
        }
    }
}

private fun inferKindFromName(name: String): String {
    val lower = name.lowercase()
    val ext = if ('.' in lower) ".${lower.substringAfterLast('.')}" else ""
    return when (ext) {
        ".md", ".markdown" -> "markdown"
        ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".heic", ".svg" -> "image"
        ".pdf" -> "pdf"
        ".xlsx", ".csv", ".tsv" -> "excel"
        ".docx", ".doc", ".rtf" -> "word"
        ".pptx" -> "pptx"
        ".mp4", ".mov", ".m4v", ".webm", ".mkv" -> "video"
        ".mp3", ".m4a", ".wav", ".flac", ".ogg", ".aac" -> "audio"
        ".go", ".kt", ".kts", ".java", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".c", ".h", ".cpp",
        ".swift", ".sh", ".zsh", ".bash", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".css",
        ".sql", ".txt", ".log", ".ini", ".conf", ".properties", ".gradle", ".env", ".vue", ".rb", ".php" -> "code"
        else -> if (lower in setOf("dockerfile", "makefile", "readme") || lower.startsWith(".env")) "code" else "binary"
    }
}

// =========================================================================
// 1. Code / Text / Markdown Viewer + Online Editor with Diff Pre-Check & 409 Lock
// =========================================================================

@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun TextMarkdownViewer(
    path: String,
    fileName: String,
    isMarkdown: Boolean,
    jumpLine: Int,
    onDownload: () -> Unit,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val scope = rememberCoroutineScope()
    val webViewRef = remember { AtomicReference<WebView?>(null) }

    var readRes by remember { mutableStateOf<FsReadResult?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var offsetLine by remember { mutableIntStateOf(if (jumpLine > 200) (jumpLine - 50).coerceAtLeast(1) else 1) }
    var tailMode by remember { mutableStateOf(false) }

    var editing by remember { mutableStateOf(false) }
    var draftText by remember { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    var showPreSaveDiff by remember { mutableStateOf(false) }
    var showUnsavedExit by remember { mutableStateOf(false) }
    var conflictErr by remember { mutableStateOf<FsConflictException?>(null) }

    var webReady by remember { mutableStateOf(false) }
    var wrapLines by remember { mutableStateOf(false) }
    var fontSizePx by remember { mutableIntStateOf(13) }
    var showSearch by remember { mutableStateOf(false) }
    var searchQuery by remember { mutableStateOf("") }

    val hasChanges = editing && readRes != null && draftText != readRes!!.content

    fun loadFile(lineOffset: Int = offsetLine, isTail: Boolean = tailMode) {
        loading = true
        errorMsg = null
        scope.launch {
            val r = AuditRepository.fsRead(path, offsetLine = lineOffset, limitLines = 400, tail = isTail)
            loading = false
            r.onSuccess { res ->
                readRes = res
                draftText = res.content
                offsetLine = res.offsetLine
            }.onFailure {
                errorMsg = it.message ?: "读取文件失败"
            }
        }
    }

    fun performSave(shaOverride: String? = null) {
        val cur = readRes ?: return
        saving = true
        scope.launch {
            val r = AuditRepository.fsWrite(path, draftText, shaOverride ?: cur.sha256)
            saving = false
            r.onSuccess { wr ->
                readRes = cur.copy(content = draftText, sha256 = wr.sha256, size = wr.size, modTime = wr.modTime)
                showPreSaveDiff = false
                editing = false
                onShowSnack("已保存到 Mac (${formatFileSize(wr.size)})")
            }.onFailure { err ->
                if (err is FsConflictException) {
                    showPreSaveDiff = false
                    conflictErr = err
                } else {
                    onShowSnack("保存失败: ${err.message}")
                }
            }
        }
    }

    LaunchedEffect(path) { loadFile() }

    LaunchedEffect(webReady, readRes, p.isDark, wrapLines, fontSizePx, editing) {
        if (editing || !webReady) return@LaunchedEffect
        val wv = webViewRef.get() ?: return@LaunchedEffect
        val res = readRes ?: return@LaunchedEffect
        if (isMarkdown) {
            wv.evaluateJavascript("window.renderMarkdown(${JSONObject.quote(res.content)}, ${p.isDark});", null)
        } else {
            val js = "window.renderCode(${JSONObject.quote(res.content)}, ${JSONObject.quote(res.ext)}, ${res.offsetLine}, $jumpLine, ${p.isDark});" +
                "window.setWrap($wrapLines); window.setFontSize($fontSizePx);"
            wv.evaluateJavascript(js, null)
        }
    }

    if (showPreSaveDiff && readRes != null) {
        val diffText = remember(readRes!!.content, draftText) {
            computeSimpleUnifiedDiff(fileName, readRes!!.content, draftText)
        }
        DiffViewerScreen(
            title = "预检改动 · $fileName",
            relFile = fileName,
            precomputedDiff = diffText.ifBlank { "无内容变化" },
            onConfirmSave = { performSave() },
            onBack = { showPreSaveDiff = false },
            onShowSnack = onShowSnack
        )
        return
    }

    BackHandler {
        if (hasChanges) showUnsavedExit = true else onBack()
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(p.bg)
            .imePadding()
            .navigationBarsPadding()
    ) {
        // Top Header
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(p.card)
                .statusBarsPadding()
                .padding(horizontal = 6.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            IconButton(onClick = { if (hasChanges) showUnsavedExit = true else onBack() }) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回", tint = p.accent)
            }
            Column(modifier = Modifier.weight(1f)) {
                Text(fileName, color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val res = readRes
                if (res != null) {
                    val sub = buildString {
                        append(formatFileSize(res.size))
                        append(" · ${res.totalLines} 行")
                        if (res.truncated) append(" · 分页只读")
                        if (hasChanges) append(" · 未保存")
                    }
                    Text(sub, color = if (hasChanges) p.orange else p.secondaryLabel, fontSize = 11.sp, maxLines = 1)
                }
            }
            if (!editing) {
                IconButton(onClick = onDownload) {
                    Icon(Icons.Default.Download, contentDescription = "下载到手机", tint = p.accent)
                }
                if (!isMarkdown) {
                    IconButton(onClick = { showSearch = !showSearch }) {
                        Icon(Icons.Default.Search, contentDescription = "页内搜索", tint = if (showSearch) p.accent else p.secondaryLabel)
                    }
                    IconButton(onClick = { wrapLines = !wrapLines }) {
                        Icon(Icons.AutoMirrored.Filled.WrapText, contentDescription = "换行", tint = if (wrapLines) p.accent else p.secondaryLabel)
                    }
                }
                if (readRes?.editable == true) {
                    IconButton(onClick = {
                        draftText = readRes?.content.orEmpty()
                        editing = true
                    }) {
                        Icon(Icons.Default.Edit, contentDescription = "编辑", tint = p.accent)
                    }
                }
            } else {
                IconButton(onClick = { showPreSaveDiff = true }, enabled = hasChanges && !saving) {
                    Icon(Icons.Default.CompareArrows, contentDescription = "预检 Diff", tint = if (hasChanges) p.accent else p.tertiaryLabel)
                }
                IconButton(onClick = { performSave() }, enabled = hasChanges && !saving) {
                    Icon(Icons.Default.Save, contentDescription = "保存", tint = if (hasChanges) p.green else p.tertiaryLabel)
                }
                IconButton(onClick = { editing = false }) {
                    Icon(Icons.Default.Visibility, contentDescription = "预览", tint = p.secondaryLabel)
                }
            }
        }

        if (isMarkdown && readRes?.editable == true) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .padding(start = 16.dp, end = 16.dp, bottom = 10.dp)
            ) {
                IosSegmentedControl(
                    options = listOf("preview" to "Markdown 渲染", "edit" to "源码编辑"),
                    selectedKey = if (editing) "edit" else "preview",
                    onSelect = { editing = (it == "edit") }
                )
            }
        }

        if (showSearch && !editing && !isMarkdown) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .padding(start = 14.dp, end = 14.dp, bottom = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                OutlinedTextField(
                    value = searchQuery,
                    onValueChange = { q ->
                        searchQuery = q
                        webViewRef.get()?.evaluateJavascript("window.searchInCode(${JSONObject.quote(q)});", null)
                    },
                    placeholder = { Text("在当前文件中查找...", fontSize = 13.sp) },
                    singleLine = true,
                    shape = RoundedCornerShape(10.dp),
                    modifier = Modifier.weight(1f),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = p.accent,
                        unfocusedBorderColor = p.separator,
                        focusedTextColor = p.label,
                        unfocusedTextColor = p.label
                    )
                )
                TextButton(onClick = { fontSizePx = (fontSizePx - 1).coerceAtLeast(10) }) { Text("A-", color = p.accent) }
                TextButton(onClick = { fontSizePx = (fontSizePx + 1).coerceAtMost(20) }) { Text("A+", color = p.accent) }
            }
        }

        HorizontalDivider(thickness = 0.5.dp, color = p.separator)

        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(color = p.accent, modifier = Modifier.size(28.dp))
                }
                errorMsg != null -> Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
                    Text(errorMsg!!, color = p.red, fontSize = 14.sp)
                }
                editing -> {
                    Box(
                        modifier = Modifier
                            .fillMaxSize()
                            .background(p.codeBg)
                            .verticalScroll(rememberScrollState())
                            .padding(14.dp)
                    ) {
                        BasicTextField(
                            value = draftText,
                            onValueChange = { draftText = it },
                            textStyle = TextStyle(
                                color = p.codeFg,
                                fontFamily = FontFamily.Monospace,
                                fontSize = 13.sp,
                                lineHeight = 20.sp
                            ),
                            cursorBrush = SolidColor(p.accent),
                            modifier = Modifier.fillMaxSize()
                        )
                    }
                }
                else -> {
                    val assetUrl = if (isMarkdown) "file:///android_asset/viewer/markdown.html" else "file:///android_asset/viewer/code.html"
                    AndroidView(
                        modifier = Modifier.fillMaxSize(),
                        factory = { ctx ->
                            WebView(ctx).apply {
                                layoutParams = ViewGroup.LayoutParams(
                                    ViewGroup.LayoutParams.MATCH_PARENT,
                                    ViewGroup.LayoutParams.MATCH_PARENT
                                )
                                settings.javaScriptEnabled = true
                                settings.cacheMode = WebSettings.LOAD_NO_CACHE
                                settings.builtInZoomControls = true
                                settings.displayZoomControls = false
                                setBackgroundColor(if (p.isDark) 0xFF000000.toInt() else 0xFFFFFFFF.toInt())
                                webViewClient = object : WebViewClient() {
                                    override fun onPageFinished(view: WebView?, url: String?) {
                                        webReady = true
                                    }
                                }
                                webViewRef.set(this)
                                loadUrl(assetUrl)
                            }
                        }
                    )
                }
            }
        }

        // Large file pagination bar
        val res = readRes
        if (!editing && res != null && res.truncated) {
            HorizontalDivider(thickness = 0.5.dp, color = p.separator)
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .padding(horizontal = 12.dp, vertical = 8.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                TextButton(
                    enabled = res.offsetLine > 1,
                    onClick = {
                        tailMode = false
                        loadFile((res.offsetLine - 400).coerceAtLeast(1), false)
                    }
                ) { Text("上一页", color = if (res.offsetLine > 1) p.accent else p.tertiaryLabel) }
                Text(
                    "L${res.offsetLine}-${res.offsetLine + res.returnedLines - 1} / ${res.totalLines}",
                    color = p.secondaryLabel,
                    fontSize = 12.sp
                )
                Row {
                    TextButton(
                        enabled = res.hasMore,
                        onClick = {
                            tailMode = false
                            loadFile(res.offsetLine + 400, false)
                        }
                    ) { Text("下一页", color = if (res.hasMore) p.accent else p.tertiaryLabel) }
                    TextButton(onClick = {
                        tailMode = true
                        loadFile(1, true)
                    }) { Text("末尾 Tail", color = p.orange) }
                }
            }
        }
    }

    if (showUnsavedExit) {
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { showUnsavedExit = false },
            title = { Text("有未保存的修改", fontWeight = FontWeight.SemiBold) },
            text = { Text("离开将丢弃当前对 $fileName 的编辑内容。", color = p.secondaryLabel, fontSize = 13.sp) },
            confirmButton = {
                IosButton(text = "放弃并退出", containerColor = p.red, onClick = { showUnsavedExit = false; onBack() })
            },
            dismissButton = {
                TextButton(onClick = { showUnsavedExit = false }) { Text("继续编辑", color = p.accent) }
            }
        )
    }

    conflictErr?.let { ce ->
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { conflictErr = null },
            title = { Text("保存冲突 (409)", fontWeight = FontWeight.SemiBold) },
            text = {
                Text(
                    "Mac 上的 $fileName 在你编辑期间已被外部或 AI 修改。为避免意外覆盖，请选择处理方式：",
                    color = p.secondaryLabel,
                    fontSize = 13.sp
                )
            },
            confirmButton = {
                IosButton(
                    text = "强制覆盖",
                    containerColor = p.red,
                    onClick = {
                        val sha = ce.currentSha256
                        conflictErr = null
                        performSave(shaOverride = sha)
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = {
                    conflictErr = null
                    loadFile()
                    editing = false
                    onShowSnack("已重新加载 Mac 最新版本")
                }) { Text("放弃并刷新", color = p.accent) }
            }
        )
    }
}

// =========================================================================
// 2. Image Pure-Memory Stream Viewer (Pinch-Zoom + Original Toggle)
// =========================================================================

@Composable
private fun ImageStreamViewer(
    path: String,
    fileName: String,
    onDownload: () -> Unit,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    var originalMode by remember { mutableStateOf(false) }
    var bitmap by remember { mutableStateOf<Bitmap?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var byteCount by remember { mutableIntStateOf(0) }

    BackHandler { onBack() }

    LaunchedEffect(path, originalMode) {
        loading = true
        errorMsg = null
        val url = AuditRepository.buildStreamUrl(path, kind = "image", maxDim = if (originalMode) 0 else 1800)
        AuditRepository.fetchStreamBytes(url)
            .onSuccess { bytes ->
                byteCount = bytes.size
                val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
                if (bmp == null) errorMsg = "无法解码图片格式" else bitmap = bmp
            }
            .onFailure { errorMsg = it.message ?: "加载图片失败" }
        loading = false
    }

    Column(modifier = Modifier.fillMaxSize().background(Color.Black).navigationBarsPadding()) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(Color(0xFF121214))
                .statusBarsPadding()
                .padding(horizontal = 6.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            IconButton(onClick = onBack) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回", tint = Color(0xFF0A84FF))
            }
            Column(modifier = Modifier.weight(1f)) {
                Text(fileName, color = Color.White, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val sub = if (byteCount > 0) "${if (originalMode) "原图流" else "高清流"} · ${formatFileSize(byteCount.toLong())} · 仅内存预览" else "流式加载中..."
                Text(sub, color = Color(0xFF98989F), fontSize = 11.sp)
            }
            TextButton(onClick = {
                originalMode = !originalMode
                onShowSnack(if (originalMode) "正在加载无损原图流..." else "已切回压缩流")
            }) {
                Text(if (originalMode) "切回压缩" else "看原图", color = Color(0xFF0A84FF), fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
            }
            IconButton(onClick = onDownload) {
                Icon(Icons.Default.Download, contentDescription = "下载原图", tint = Color(0xFF0A84FF))
            }
        }

        Box(modifier = Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
            when {
                loading -> CircularProgressIndicator(color = Color(0xFF0A84FF))
                errorMsg != null -> Text(errorMsg!!, color = p.red, fontSize = 14.sp)
                bitmap != null -> ZoomableBitmapView(bitmap!!)
            }
        }
    }
}

@Composable
private fun ZoomableBitmapView(bitmap: Bitmap) {
    var scale by remember(bitmap) { mutableFloatStateOf(1f) }
    var offset by remember(bitmap) { mutableStateOf(Offset.Zero) }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .pointerInput(bitmap) {
                detectTransformGestures { _, pan, zoom, _ ->
                    val newScale = (scale * zoom).coerceIn(1f, 5f)
                    scale = newScale
                    offset = if (newScale > 1f) offset + pan else Offset.Zero
                }
            }
            .pointerInput(bitmap) {
                detectTapGestures(
                    onDoubleTap = {
                        if (scale > 1.2f) {
                            scale = 1f
                            offset = Offset.Zero
                        } else {
                            scale = 2.5f
                        }
                    }
                )
            },
        contentAlignment = Alignment.Center
    ) {
        Image(
            bitmap = bitmap.asImageBitmap(),
            contentDescription = null,
            contentScale = ContentScale.Fit,
            modifier = Modifier
                .fillMaxSize()
                .graphicsLayer {
                    scaleX = scale
                    scaleY = scale
                    translationX = offset.x
                    translationY = offset.y
                }
        )
    }
}

// =========================================================================
// 3. PDF Single-Page Stream Viewer (poppler pdfinfo + pdftoppm)
// =========================================================================

@Composable
private fun PdfStreamViewer(
    path: String,
    fileName: String,
    onDownload: () -> Unit,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    var meta by remember { mutableStateOf<PdfMeta?>(null) }
    var curPage by remember { mutableIntStateOf(1) }
    var highDpi by remember { mutableStateOf(false) }
    var pageBitmap by remember { mutableStateOf<Bitmap?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var showJumpDialog by remember { mutableStateOf(false) }
    var jumpInput by remember { mutableStateOf("") }

    // Tiny pure-RAM LRU cache (max 3 pages) cleared immediately on exit
    val ramCache = remember(path) { LinkedHashMap<String, Bitmap>(4, 0.75f, true) }
    DisposableEffect(path) {
        onDispose { ramCache.clear() }
    }

    BackHandler { onBack() }

    LaunchedEffect(path) {
        loading = true
        AuditRepository.fsPdfMeta(path)
            .onSuccess { meta = it }
            .onFailure {
                errorMsg = it.message ?: "读取 PDF 信息失败"
                loading = false
            }
    }

    LaunchedEffect(meta, curPage, highDpi) {
        val m = meta ?: return@LaunchedEffect
        val dpi = if (highDpi) 220 else 160
        val key = "$curPage@$dpi"
        val cached = ramCache[key]
        if (cached != null) {
            pageBitmap = cached
            loading = false
            errorMsg = null
            return@LaunchedEffect
        }
        loading = true
        errorMsg = null
        val url = AuditRepository.buildStreamUrl(path, kind = "pdf_page", page = curPage.coerceIn(1, m.pages), dpi = dpi)
        AuditRepository.fetchStreamBytes(url)
            .onSuccess { bytes ->
                val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
                if (bmp == null) {
                    errorMsg = "PDF 第 $curPage 页解码失败"
                } else {
                    if (ramCache.size >= 3) {
                        val oldest = ramCache.keys.firstOrNull()
                        if (oldest != null) ramCache.remove(oldest)
                    }
                    ramCache[key] = bmp
                    pageBitmap = bmp
                }
            }
            .onFailure { errorMsg = it.message ?: "渲染 PDF 第 $curPage 页失败" }
        loading = false
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg).navigationBarsPadding()) {
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
                Text(fileName, color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val sub = if (meta != null) "第 $curPage / ${meta!!.pages} 页 · ${formatFileSize(meta!!.size)} · pdftoppm 流式" else "解析 PDF..."
                Text(sub, color = p.secondaryLabel, fontSize = 11.sp)
            }
            IconButton(onClick = {
                highDpi = !highDpi
                onShowSnack(if (highDpi) "已切换高清 220 DPI" else "已切换标准 160 DPI")
            }) {
                Icon(Icons.Default.Hd, contentDescription = "高清 DPI", tint = if (highDpi) p.accent else p.secondaryLabel)
            }
            IconButton(onClick = onDownload) {
                Icon(Icons.Default.Download, contentDescription = "下载 PDF", tint = p.accent)
            }
        }
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)

        Box(
            modifier = Modifier.weight(1f).fillMaxWidth().background(if (p.isDark) Color(0xFF111113) else Color(0xFFE5E5EA)),
            contentAlignment = Alignment.Center
        ) {
            if (pageBitmap != null) {
                ZoomableBitmapView(pageBitmap!!)
            }
            if (loading) {
                Surface(shape = RoundedCornerShape(12.dp), color = p.card.copy(alpha = 0.9f)) {
                    Row(
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        CircularProgressIndicator(color = p.accent, modifier = Modifier.size(20.dp), strokeWidth = 2.dp)
                        Text("渲染第 $curPage 页...", color = p.label, fontSize = 13.sp)
                    }
                }
            }
            if (errorMsg != null && !loading) {
                Text(errorMsg!!, color = p.red, fontSize = 14.sp, modifier = Modifier.padding(24.dp))
            }
        }

        val totalPages = meta?.pages ?: 1
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(p.card)
                .padding(horizontal = 12.dp, vertical = 6.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            IconButton(enabled = curPage > 1, onClick = { curPage-- }) {
                Icon(Icons.AutoMirrored.Filled.KeyboardArrowLeft, contentDescription = "上一页", tint = if (curPage > 1) p.accent else p.tertiaryLabel)
            }
            Surface(
                shape = RoundedCornerShape(8.dp),
                color = p.secondaryFill,
                modifier = Modifier.clickable {
                    jumpInput = curPage.toString()
                    showJumpDialog = true
                }
            ) {
                Text(
                    "第 $curPage / $totalPages 页（点击跳转）",
                    color = p.label,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.padding(horizontal = 14.dp, vertical = 7.dp)
                )
            }
            IconButton(enabled = curPage < totalPages, onClick = { curPage++ }) {
                Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, contentDescription = "下一页", tint = if (curPage < totalPages) p.accent else p.tertiaryLabel)
            }
        }
    }

    if (showJumpDialog) {
        val totalPages = meta?.pages ?: 1
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { showJumpDialog = false },
            title = { Text("跳转到指定页 (1-$totalPages)", fontWeight = FontWeight.SemiBold) },
            text = {
                OutlinedTextField(
                    value = jumpInput,
                    onValueChange = { jumpInput = it.filter { c -> c.isDigit() } },
                    singleLine = true,
                    label = { Text("页码") },
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                IosButton(text = "跳转", onClick = {
                    val n = jumpInput.toIntOrNull()
                    if (n != null && n in 1..totalPages) curPage = n
                    showJumpDialog = false
                })
            },
            dismissButton = {
                TextButton(onClick = { showJumpDialog = false }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }
}

// =========================================================================
// 4. Excel / CSV Multi-Sheet Table Viewer & Cell Editor
// =========================================================================

@Composable
private fun ExcelViewer(
    path: String,
    fileName: String,
    onDownload: () -> Unit,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val scope = rememberCoroutineScope()

    var meta by remember { mutableStateOf<ExcelMeta?>(null) }
    var activeSheet by remember { mutableStateOf("") }
    var rowStart by remember { mutableIntStateOf(1) }
    var sheetData by remember { mutableStateOf<ExcelSheetData?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }

    var editingCell by remember { mutableStateOf<Triple<Int, Int, String>?>(null) } // row1, col1, curValue
    var cellDraft by remember { mutableStateOf("") }

    BackHandler { onBack() }

    fun loadSheet(sheet: String, start: Int) {
        loading = true
        errorMsg = null
        scope.launch {
            AuditRepository.fsExcelSheet(path, sheet, start, 150)
                .onSuccess { sheetData = it }
                .onFailure { errorMsg = it.message ?: "读取工作表失败" }
            loading = false
        }
    }

    LaunchedEffect(path) {
        loading = true
        AuditRepository.fsExcelMeta(path)
            .onSuccess { m ->
                meta = m
                activeSheet = m.sheets.firstOrNull().orEmpty()
                loadSheet(activeSheet, 1)
            }
            .onFailure {
                errorMsg = it.message ?: "解析表格失败"
                loading = false
            }
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg).navigationBarsPadding()) {
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
                Text(fileName, color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val sd = sheetData
                Text(
                    if (sd != null) "${sd.sheet} · 共 ${sd.totalRows} 行 × ${sd.totalCols} 列 · 点击单元格可编辑" else "加载表格中...",
                    color = p.secondaryLabel,
                    fontSize = 11.sp
                )
            }
            IconButton(onClick = onDownload) {
                Icon(Icons.Default.Download, contentDescription = "下载表格", tint = p.accent)
            }
        }

        // Sheet tabs
        val sheets = meta?.sheets.orEmpty()
        if (sheets.size > 1) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .horizontalScroll(rememberScrollState())
                    .padding(start = 14.dp, end = 14.dp, bottom = 10.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                sheets.forEach { sh ->
                    val sel = sh == activeSheet
                    Surface(
                        shape = RoundedCornerShape(8.dp),
                        color = if (sel) p.accent else p.secondaryFill,
                        modifier = Modifier.clickable {
                            activeSheet = sh
                            rowStart = 1
                            loadSheet(sh, 1)
                        }
                    ) {
                        Text(
                            sh,
                            color = if (sel) Color.White else p.label,
                            fontSize = 12.sp,
                            fontWeight = if (sel) FontWeight.SemiBold else FontWeight.Normal,
                            modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp)
                        )
                    }
                }
            }
        }
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)

        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(color = p.accent, modifier = Modifier.size(28.dp))
                }
                errorMsg != null -> Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
                    Text(errorMsg!!, color = p.red, fontSize = 14.sp)
                }
                sheetData != null -> {
                    val sd = sheetData!!
                    val hScroll = rememberScrollState()
                    LazyColumn(modifier = Modifier.fillMaxSize()) {
                        // Sticky column headers
                        item {
                            Row(
                                modifier = Modifier
                                    .horizontalScroll(hScroll)
                                    .background(p.secondaryFill)
                            ) {
                                Box(
                                    modifier = Modifier.width(44.dp).border(0.5.dp, p.separator).padding(vertical = 8.dp),
                                    contentAlignment = Alignment.Center
                                ) {
                                    Text("#", color = p.secondaryLabel, fontSize = 11.sp, fontWeight = FontWeight.Bold)
                                }
                                for (c in 0 until sd.totalCols) {
                                    Box(
                                        modifier = Modifier.width(120.dp).border(0.5.dp, p.separator).padding(horizontal = 8.dp, vertical = 8.dp),
                                        contentAlignment = Alignment.Center
                                    ) {
                                        Text(colIndexToLetter(c + 1), color = p.secondaryLabel, fontSize = 11.sp, fontWeight = FontWeight.Bold)
                                    }
                                }
                            }
                        }
                        itemsIndexed(sd.rows) { idx, row ->
                            val rNum = sd.rowStart + idx
                            Row(
                                modifier = Modifier
                                    .horizontalScroll(hScroll)
                                    .background(if (idx % 2 == 0) p.card else p.bg)
                            ) {
                                Box(
                                    modifier = Modifier.width(44.dp).border(0.5.dp, p.separator.copy(alpha = 0.5f)).padding(vertical = 9.dp),
                                    contentAlignment = Alignment.Center
                                ) {
                                    Text("$rNum", color = p.tertiaryLabel, fontSize = 11.sp)
                                }
                                for (c in 0 until sd.totalCols) {
                                    val cellVal = row.getOrElse(c) { "" }
                                    Box(
                                        modifier = Modifier
                                            .width(120.dp)
                                            .border(0.5.dp, p.separator.copy(alpha = 0.45f))
                                            .clickable {
                                                cellDraft = cellVal
                                                editingCell = Triple(rNum, c + 1, cellVal)
                                            }
                                            .padding(horizontal = 8.dp, vertical = 9.dp)
                                    ) {
                                        Text(
                                            text = cellVal.ifEmpty { " " },
                                            color = p.label,
                                            fontSize = 12.sp,
                                            fontWeight = if (rNum == 1) FontWeight.SemiBold else FontWeight.Normal,
                                            maxLines = 2,
                                            overflow = TextOverflow.Ellipsis
                                        )
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        val sd = sheetData
        if (sd != null && (sd.rowStart > 1 || sd.hasMore)) {
            HorizontalDivider(thickness = 0.5.dp, color = p.separator)
            Row(
                modifier = Modifier.fillMaxWidth().background(p.card).padding(horizontal = 12.dp, vertical = 6.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                TextButton(enabled = sd.rowStart > 1, onClick = {
                    val prev = (sd.rowStart - 150).coerceAtLeast(1)
                    rowStart = prev
                    loadSheet(activeSheet, prev)
                }) { Text("上一页 150 行", color = if (sd.rowStart > 1) p.accent else p.tertiaryLabel) }
                Text("第 ${sd.rowStart}-${sd.rowStart + sd.rows.size - 1} / ${sd.totalRows} 行", color = p.secondaryLabel, fontSize = 12.sp)
                TextButton(enabled = sd.hasMore, onClick = {
                    val next = sd.rowStart + 150
                    rowStart = next
                    loadSheet(activeSheet, next)
                }) { Text("下一页 150 行", color = if (sd.hasMore) p.accent else p.tertiaryLabel) }
            }
        }
    }

    editingCell?.let { (r1, c1, _) ->
        val coord = "${colIndexToLetter(c1)}$r1"
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { editingCell = null },
            title = { Text("编辑单元格 $coord", fontWeight = FontWeight.SemiBold) },
            text = {
                OutlinedTextField(
                    value = cellDraft,
                    onValueChange = { cellDraft = it },
                    label = { Text("单元格内容") },
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                IosButton(text = "保存", containerColor = p.green, onClick = {
                    val sd = sheetData ?: return@IosButton
                    val newVal = cellDraft
                    editingCell = null
                    scope.launch {
                        AuditRepository.fsExcelWriteCell(path, sd.sheet, r1, c1, newVal, sd.sha256)
                            .onSuccess {
                                onShowSnack("单元格 $coord 已保存到 Mac")
                                loadSheet(activeSheet, rowStart)
                            }
                            .onFailure { onShowSnack("保存失败: ${it.message}") }
                    }
                })
            },
            dismissButton = {
                TextButton(onClick = { editingCell = null }) { Text("取消", color = p.secondaryLabel) }
            }
        )
    }
}

private fun colIndexToLetter(col1: Int): String {
    var n = col1
    val sb = StringBuilder()
    while (n > 0) {
        val rem = (n - 1) % 26
        sb.insert(0, ('A'.code + rem).toChar())
        n = (n - 1) / 26
    }
    return sb.toString()
}

// =========================================================================
// 5. Word (.docx/.doc/.rtf) Viewer via macOS textutil HTML stream
// =========================================================================

@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun WordHtmlViewer(path: String, fileName: String, onDownload: () -> Unit, onBack: () -> Unit) {
    val p = LocalIosPalette.current
    val webViewRef = remember { AtomicReference<WebView?>(null) }
    var htmlBody by remember { mutableStateOf<String?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var webReady by remember { mutableStateOf(false) }

    BackHandler { onBack() }

    LaunchedEffect(path) {
        loading = true
        val url = AuditRepository.buildStreamUrl(path, kind = "docx_html")
        AuditRepository.fetchStreamText(url)
            .onSuccess { htmlBody = it }
            .onFailure { errorMsg = it.message ?: "文档转换失败" }
        loading = false
    }

    LaunchedEffect(webReady, htmlBody, p.isDark) {
        if (!webReady) return@LaunchedEffect
        val raw = htmlBody ?: return@LaunchedEffect
        webViewRef.get()?.evaluateJavascript("window.renderDocHtml(${JSONObject.quote(raw)}, ${p.isDark});", null)
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg).navigationBarsPadding()) {
        SimpleViewerHeader(title = fileName, subtitle = "macOS textutil 实时流式排版", onDownload = onDownload, onBack = onBack)
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            AndroidView(
                modifier = Modifier.fillMaxSize(),
                factory = { ctx ->
                    WebView(ctx).apply {
                        layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                        settings.javaScriptEnabled = true
                        settings.cacheMode = WebSettings.LOAD_NO_CACHE
                        setBackgroundColor(if (p.isDark) 0xFF000000.toInt() else 0xFFFFFFFF.toInt())
                        webViewClient = object : WebViewClient() {
                            override fun onPageFinished(view: WebView?, url: String?) { webReady = true }
                        }
                        webViewRef.set(this)
                        loadUrl("file:///android_asset/viewer/docx.html")
                    }
                }
            )
            if (loading) Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator(color = p.accent) }
            if (errorMsg != null) Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) { Text(errorMsg!!, color = p.red) }
        }
    }
}

// =========================================================================
// 6. PPTX Slide Outline Viewer
// =========================================================================

@Composable
private fun PptxOutlineViewer(path: String, fileName: String, onDownload: () -> Unit, onBack: () -> Unit) {
    val p = LocalIosPalette.current
    var outline by remember { mutableStateOf<PptxOutline?>(null) }
    var loading by remember { mutableStateOf(true) }
    var errorMsg by remember { mutableStateOf<String?>(null) }

    BackHandler { onBack() }

    LaunchedEffect(path) {
        loading = true
        AuditRepository.fsPptxOutline(path)
            .onSuccess { outline = it }
            .onFailure { errorMsg = it.message ?: "解析 PPTX 失败" }
        loading = false
    }

    Column(modifier = Modifier.fillMaxSize().background(p.bg).navigationBarsPadding()) {
        SimpleViewerHeader(
            title = fileName,
            subtitle = if (outline != null) "共 ${outline!!.slides.size} 页幻灯片大纲" else "解析中...",
            onDownload = onDownload,
            onBack = onBack
        )
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator(color = p.accent) }
                errorMsg != null -> Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) { Text(errorMsg!!, color = p.red) }
                outline != null -> LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)
                ) {
                    items(outline!!.slides) { slide ->
                        IosSection {
                            Column(modifier = Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    IosPillBadge("P${slide.index}", p.accent)
                                    Text(slide.title, color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
                                }
                                slide.lines.forEach { ln ->
                                    Text("• $ln", color = p.secondaryLabel, fontSize = 13.sp, lineHeight = 19.sp)
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

// =========================================================================
// 7. Audio / Video Zero-Download HTTP Range Stream Player (Media3 ExoPlayer)
// =========================================================================

@androidx.annotation.OptIn(UnstableApi::class)
@Composable
private fun MediaStreamViewer(path: String, fileName: String, isAudio: Boolean, onDownload: () -> Unit, onBack: () -> Unit) {
    val context = LocalContext.current
    val streamUrl = remember(path) { AuditRepository.buildStreamUrl(path, kind = "raw") }

    val exoPlayer = remember(streamUrl) {
        val dsFactory = OkHttpDataSource.Factory(AuditRepository.longHttpClient)
            .setDefaultRequestProperties(mapOf("Authorization" to "Bearer ${AuditRepository.config.appToken}"))
        ExoPlayer.Builder(context)
            .setMediaSourceFactory(DefaultMediaSourceFactory(dsFactory))
            .build().apply {
                setMediaItem(MediaItem.fromUri(streamUrl))
                prepare()
                playWhenReady = true
            }
    }

    DisposableEffect(exoPlayer) {
        onDispose { exoPlayer.release() }
    }
    BackHandler { onBack() }

    Column(modifier = Modifier.fillMaxSize().background(Color.Black).navigationBarsPadding()) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(Color(0xFF121214))
                .statusBarsPadding()
                .padding(horizontal = 6.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            IconButton(onClick = onBack) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回", tint = Color(0xFF0A84FF))
            }
            Column(modifier = Modifier.weight(1f)) {
                Text(fileName, color = Color.White, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(if (isAudio) "音频 HTTP Range 边播边流" else "视频 HTTP Range 边播边流", color = Color(0xFF98989F), fontSize = 11.sp)
            }
            IconButton(onClick = onDownload) {
                Icon(Icons.Default.Download, contentDescription = "下载媒体文件", tint = Color(0xFF0A84FF))
            }
        }

        Box(modifier = Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
            AndroidView(
                modifier = Modifier.fillMaxSize(),
                factory = { ctx ->
                    PlayerView(ctx).apply {
                        layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                        player = exoPlayer
                        useController = true
                        setShowBuffering(PlayerView.SHOW_BUFFERING_WHEN_PLAYING)
                    }
                }
            )
        }
    }
}

// =========================================================================
// 8. Binary Fallback Viewer
// =========================================================================

@Composable
private fun BinaryFallbackViewer(
    path: String,
    fileName: String,
    onDownload: () -> Unit,
    onTryText: () -> Unit,
    onBack: () -> Unit
) {
    val p = LocalIosPalette.current
    BackHandler { onBack() }
    Column(modifier = Modifier.fillMaxSize().background(p.bg).navigationBarsPadding()) {
        SimpleViewerHeader(title = fileName, subtitle = "二进制/未知格式", onDownload = onDownload, onBack = onBack)
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)
        Column(
            modifier = Modifier.fillMaxSize().padding(28.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center
        ) {
            Icon(Icons.Default.InsertDriveFile, contentDescription = null, tint = p.secondaryLabel, modifier = Modifier.size(54.dp))
            Spacer(Modifier.height(12.dp))
            Text(fileName, color = p.label, fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(6.dp))
            Text("该格式暂不支持直接预览，可下载到手机 Download/PhoneAiAudit 目录本地打开", color = p.secondaryLabel, fontSize = 13.sp)
            Spacer(Modifier.height(20.dp))
            IosButton(text = "下载到手机", icon = Icons.Default.Download, onClick = onDownload)
            Spacer(Modifier.height(10.dp))
            IosTintedButton(text = "尝试作为纯文本打开", onClick = onTryText)
        }
    }
}

@Composable
private fun SimpleViewerHeader(title: String, subtitle: String, onDownload: (() -> Unit)? = null, onBack: () -> Unit) {
    val p = LocalIosPalette.current
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
            Text(title, color = p.label, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(subtitle, color = p.secondaryLabel, fontSize = 11.sp, maxLines = 1)
        }
        if (onDownload != null) {
            IconButton(onClick = onDownload) {
                Icon(Icons.Default.Download, contentDescription = "下载到手机", tint = p.accent)
            }
        }
    }
}
