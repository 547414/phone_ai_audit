package com.phoneaiaudit.app.ui

import android.annotation.SuppressLint
import android.view.ViewGroup
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.WrapText
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Restore
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.GitDiffResult
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicReference

@SuppressLint("SetJavaScriptEnabled")
@Composable
fun DiffViewerScreen(
    title: String,
    repoPath: String = "",
    relFile: String = "",
    initialStaged: Boolean = false,
    commitSha: String = "",
    precomputedDiff: String? = null,
    onConfirmSave: (() -> Unit)? = null,
    onDiscarded: (() -> Unit)? = null,
    onBack: () -> Unit,
    onShowSnack: (String) -> Unit
) {
    val p = LocalIosPalette.current
    val scope = rememberCoroutineScope()
    val webViewRef = remember { AtomicReference<WebView?>(null) }

    var staged by remember(initialStaged) { mutableStateOf(initialStaged) }
    var wrapLines by remember { mutableStateOf(false) }
    var loading by remember { mutableStateOf(precomputedDiff == null) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var diffRes by remember { mutableStateOf<GitDiffResult?>(null) }
    var webReady by remember { mutableStateOf(false) }
    var showDiscardConfirm by remember { mutableStateOf(false) }

    BackHandler { onBack() }

    fun loadDiff() {
        if (precomputedDiff != null) {
            var add = 0
            var del = 0
            precomputedDiff.lines().forEach {
                if (it.startsWith("+") && !it.startsWith("+++")) add++
                else if (it.startsWith("-") && !it.startsWith("---")) del++
            }
            diffRes = GitDiffResult(
                file = relFile,
                staged = false,
                untracked = false,
                binary = false,
                truncated = false,
                added = add,
                deleted = del,
                diff = precomputedDiff
            )
            loading = false
            return
        }
        loading = true
        errorMsg = null
        scope.launch {
            val r = AuditRepository.gitDiff(repoPath, relFile, staged, commitSha)
            loading = false
            r.onSuccess { diffRes = it }
                .onFailure { errorMsg = it.message ?: "加载 Diff 失败" }
        }
    }

    LaunchedEffect(repoPath, relFile, staged, commitSha, precomputedDiff) {
        loadDiff()
    }

    LaunchedEffect(webReady, diffRes, p.isDark, wrapLines) {
        if (!webReady) return@LaunchedEffect
        val wv = webViewRef.get() ?: return@LaunchedEffect
        val text = diffRes?.diff ?: return@LaunchedEffect
        val js = "window.renderDiff(${JSONObject.quote(text)}, ${p.isDark}); window.setWrap($wrapLines);"
        wv.evaluateJavascript(js, null)
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(p.bg)
            .navigationBarsPadding()
    ) {
        // Top navigation bar
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
                Text(
                    text = title,
                    color = p.label,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
                val dr = diffRes
                if (dr != null) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                        if (dr.added > 0) Text("+${dr.added}", color = p.green, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
                        if (dr.deleted > 0) Text("-${dr.deleted}", color = p.red, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
                        val modeLabel = when {
                            precomputedDiff != null -> "保存前改动预检"
                            commitSha.isNotBlank() -> "Commit ${commitSha.take(7)}"
                            dr.untracked -> "未跟踪新文件"
                            dr.staged -> "暂存区 (Staged)"
                            else -> "工作区改动"
                        }
                        Text(modeLabel, color = p.secondaryLabel, fontSize = 11.sp, maxLines = 1)
                    }
                }
            }
            IconButton(onClick = { wrapLines = !wrapLines }) {
                Icon(
                    Icons.AutoMirrored.Filled.WrapText,
                    contentDescription = "换行切换",
                    tint = if (wrapLines) p.accent else p.secondaryLabel
                )
            }
            if (precomputedDiff == null && commitSha.isBlank() && relFile.isNotBlank()) {
                IconButton(onClick = { showDiscardConfirm = true }) {
                    Icon(Icons.Default.Restore, contentDescription = "撤销改动", tint = p.red)
                }
            }
        }

        if (precomputedDiff == null && commitSha.isBlank() && relFile.isNotBlank() && diffRes?.untracked != true) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .padding(start = 16.dp, end = 16.dp, bottom = 10.dp)
            ) {
                IosSegmentedControl(
                    options = listOf("wt" to "工作区改动", "staged" to "暂存区 Staged"),
                    selectedKey = if (staged) "staged" else "wt",
                    onSelect = { staged = (it == "staged") }
                )
            }
        }
        HorizontalDivider(thickness = 0.5.dp, color = p.separator)

        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
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
                        loadUrl("file:///android_asset/viewer/diff.html")
                    }
                }
            )

            if (loading) {
                Box(modifier = Modifier.fillMaxSize().background(p.bg.copy(alpha = 0.7f)), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(color = p.accent, modifier = Modifier.size(28.dp))
                }
            }
            if (errorMsg != null) {
                Box(modifier = Modifier.fillMaxSize().background(p.bg).padding(24.dp), contentAlignment = Alignment.Center) {
                    Text(errorMsg!!, color = p.red, fontSize = 14.sp)
                }
            }
        }

        if (onConfirmSave != null) {
            HorizontalDivider(thickness = 0.5.dp, color = p.separator)
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(p.card)
                    .padding(horizontal = 16.dp, vertical = 10.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                IosTintedButton(
                    text = "继续编辑",
                    tint = p.secondaryLabel,
                    onClick = onBack,
                    modifier = Modifier.weight(1f)
                )
                IosButton(
                    text = "确认保存到 Mac",
                    icon = Icons.Default.Check,
                    containerColor = p.green,
                    onClick = onConfirmSave,
                    modifier = Modifier.weight(1.4f)
                )
            }
        }
    }

    if (showDiscardConfirm) {
        AlertDialog(
            containerColor = p.card,
            titleContentColor = p.label,
            textContentColor = p.label,
            onDismissRequest = { showDiscardConfirm = false },
            title = { Text("撤销文件改动？", fontWeight = FontWeight.SemiBold) },
            text = {
                Text(
                    "将还原 $relFile 的改动（未跟踪新文件将移至 Mac 废纸篓，不会彻底抹除）。确定继续？",
                    color = p.secondaryLabel,
                    fontSize = 13.sp
                )
            },
            confirmButton = {
                IosButton(
                    text = "确定撤销",
                    containerColor = p.red,
                    onClick = {
                        showDiscardConfirm = false
                        scope.launch {
                            AuditRepository.gitDiscard(repoPath, relFile)
                                .onSuccess {
                                    onShowSnack("已撤销 $relFile 的改动")
                                    onDiscarded?.invoke()
                                    onBack()
                                }
                                .onFailure { onShowSnack("撤销失败: ${it.message}") }
                        }
                    }
                )
            },
            dismissButton = {
                TextButton(onClick = { showDiscardConfirm = false }) {
                    Text("取消", color = p.secondaryLabel)
                }
            }
        )
    }
}

/**
 * Lightweight LCS-based unified diff for pre-save edit inspection (zero external dependency).
 */
fun computeSimpleUnifiedDiff(fileName: String, oldText: String, newText: String): String {
    if (oldText == newText) return ""
    val a = oldText.replace("\r\n", "\n").split("\n")
    val b = newText.replace("\r\n", "\n").split("\n")
    // Guard against huge files for O(N*M) LCS: fallback to chunked prefix/suffix diff
    var start = 0
    while (start < a.size && start < b.size && a[start] == b[start]) start++
    var endA = a.size - 1
    var endB = b.size - 1
    while (endA >= start && endB >= start && a[endA] == b[endB]) {
        endA--
        endB--
    }
    val subA = if (start <= endA) a.subList(start, endA + 1) else emptyList()
    val subB = if (start <= endB) b.subList(start, endB + 1) else emptyList()

    val sb = StringBuilder()
    sb.append("--- a/").append(fileName).append("\n")
    sb.append("+++ b/").append(fileName).append("\n")

    val ctxBefore = (start - 3).coerceAtLeast(0)
    val ctxAfterA = (endA + 3).coerceAtMost(a.size - 1)
    val oldStart = ctxBefore + 1
    val oldCount = (ctxAfterA - ctxBefore + 1).coerceAtLeast(0)
    val newCount = (oldCount - subA.size + subB.size).coerceAtLeast(0)
    sb.append("@@ -").append(oldStart).append(",").append(oldCount)
        .append(" +").append(oldStart).append(",").append(newCount).append(" @@\n")

    for (i in ctxBefore until start) sb.append(" ").append(a[i]).append("\n")

    if (subA.size * subB.size <= 160_000) {
        val m = subA.size
        val n = subB.size
        val dp = Array(m + 1) { IntArray(n + 1) }
        for (i in m - 1 downTo 0) {
            for (j in n - 1 downTo 0) {
                dp[i][j] = if (subA[i] == subB[j]) dp[i + 1][j + 1] + 1 else maxOf(dp[i + 1][j], dp[i][j + 1])
            }
        }
        var i = 0
        var j = 0
        while (i < m || j < n) {
            when {
                i < m && j < n && subA[i] == subB[j] -> {
                    sb.append(" ").append(subA[i]).append("\n"); i++; j++
                }
                i < m && (j == n || dp[i + 1][j] >= dp[i][j + 1]) -> {
                    sb.append("-").append(subA[i]).append("\n"); i++
                }
                else -> {
                    sb.append("+").append(subB[j]).append("\n"); j++
                }
            }
        }
    } else {
        subA.forEach { sb.append("-").append(it).append("\n") }
        subB.forEach { sb.append("+").append(it).append("\n") }
    }

    for (i in (endA + 1)..ctxAfterA) sb.append(" ").append(a[i]).append("\n")
    return sb.toString()
}
