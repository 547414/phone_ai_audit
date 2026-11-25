package com.phoneaiaudit.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.UsageAgentItem
import com.phoneaiaudit.app.data.UsageDailyPoint
import com.phoneaiaudit.app.data.UsageQuotaWindow
import com.phoneaiaudit.app.data.UsageSummary
import com.phoneaiaudit.app.data.formatFileSize
import com.phoneaiaudit.app.data.formatTokenCount
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale

private val ClaudeColor = Color(0xFFD97757)
private val CodexColor = Color(0xFF10A37F)
private val AgyColor = Color(0xFF4F7CFF)

@Composable
fun UsageScreen(onShowSnack: (String) -> Unit) {
    val p = LocalIosPalette.current
    val scope = rememberCoroutineScope()
    val summary by AuditRepository.usageSummary.collectAsState()
    val status by AuditRepository.status.collectAsState()

    var loading by remember { mutableStateOf(false) }
    var errorMsg by remember { mutableStateOf<String?>(null) }
    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    var selectedDayIndex by remember { mutableStateOf<Int?>(null) }

    fun refresh(force: Boolean) {
        if (loading) return
        loading = true
        errorMsg = null
        scope.launch {
            AuditRepository.fetchUsage(force = force)
                .onSuccess { errorMsg = null }
                .onFailure {
                    errorMsg = it.message ?: "拉取失败"
                    if (force) onShowSnack("刷新用量失败: ${it.message}")
                }
            loading = false
        }
    }

    LaunchedEffect(Unit) {
        val cached = summary
        if (cached == null || System.currentTimeMillis() - cached.generatedAt > 30_000L) {
            refresh(force = false)
        }
    }

    // Live 1-second ticker for 5h / weekly countdown display while screen is visible.
    LaunchedEffect(Unit) {
        while (true) {
            delay(1000L)
            nowMs = System.currentTimeMillis()
        }
    }

    val subtitleText = remember(summary, status.macOnline) {
        val s = summary
        when {
            !status.macOnline && s == null -> "Mac 当前离线"
            s != null -> "${s.hostname.ifBlank { "Mac" }} · 增量扫描 ${s.meta.scanMs}ms · 缓存 ${s.meta.filesCached}/${s.meta.filesTotal}"
            else -> "实时查看各 Agent Token 用量与重置倒计时"
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(p.bg)
    ) {
        IosPageHeader(
            title = "用量",
            subtitle = subtitleText,
            trailing = {
                IosTintedButton(
                    text = if (loading) "刷新中" else "刷新",
                    icon = Icons.Default.Refresh,
                    enabled = !loading,
                    onClick = { refresh(force = true) }
                )
            }
        )

        if (summary == null && loading) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    CircularProgressIndicator(color = p.accent, strokeWidth = 2.5.dp)
                    Text("正在通过加密隧道增量统计 Mac 日志...", color = p.secondaryLabel, fontSize = 13.sp)
                }
            }
            return
        }

        if (summary == null && errorMsg != null) {
            Box(modifier = Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
                IosSection {
                    Column(
                        modifier = Modifier.fillMaxWidth().padding(20.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                        verticalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        Text("暂无法获取 Mac 用量统计", color = p.label, fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
                        Text(errorMsg.orEmpty(), color = p.secondaryLabel, fontSize = 13.sp, textAlign = TextAlign.Center)
                        IosButton(text = "重新拉取", onClick = { refresh(force = true) })
                    }
                }
            }
            return
        }

        val data = summary ?: return

        LazyColumn(
            modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 28.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            if (!errorMsg.isNullOrBlank()) {
                item {
                    IosSection {
                        Text(
                            text = "上次刷新失败：$errorMsg（当前展示缓存快照）",
                            color = p.orange,
                            fontSize = 12.sp,
                            modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp)
                        )
                    }
                }
            }

            // 1. Combined Overview KPIs
            item {
                OverviewSection(data)
            }

            // 2. Per-Agent Quota & Usage Cards
            items(data.agents, key = { it.agent }) { agent ->
                AgentUsageCard(agent = agent, nowMs = nowMs)
            }

            // 3. 14-Day Stacked Bar Chart
            if (data.daily.isNotEmpty()) {
                item {
                    DailyChartSection(
                        daily = data.daily,
                        selectedIndex = selectedDayIndex,
                        onSelect = { idx -> selectedDayIndex = if (selectedDayIndex == idx) null else idx }
                    )
                }
            }

            // 4. Web Dashboard & Cache Footnote
            item {
                val webHint = if (data.webUrl.isNotBlank()) {
                    "Mac 端也可直接打开网页看板：${data.webUrl}（命令行执行 ai-audit web）"
                } else {
                    "Mac 端增量缓存体积：${formatFileSize(data.meta.cacheBytes)}"
                }
                Text(
                    text = "$webHint\n聚合缓存 ${formatFileSize(data.meta.cacheBytes)} · 更新于 ${formatClockTime(data.generatedAt)}",
                    color = p.secondaryLabel,
                    fontSize = 12.sp,
                    lineHeight = 17.sp,
                    modifier = Modifier.padding(horizontal = 8.dp)
                )
            }
        }
    }
}

@Composable
private fun OverviewSection(data: UsageSummary) {
    val p = LocalIosPalette.current
    IosSection(title = "总览（全部 Agent 汇总）") {
        Column(
            modifier = Modifier.fillMaxWidth().padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                KpiTile(
                    label = "近 5 小时",
                    value = formatTokenCount(data.fiveHour.totalTokens),
                    sub = "${data.fiveHour.requests} 次",
                    modifier = Modifier.weight(1f)
                )
                KpiTile(
                    label = "今日用量",
                    value = formatTokenCount(data.today.totalTokens),
                    sub = "${data.today.requests} 次",
                    modifier = Modifier.weight(1f)
                )
                KpiTile(
                    label = "本周用量",
                    value = formatTokenCount(data.week.totalTokens),
                    sub = "${data.week.requests} 次",
                    modifier = Modifier.weight(1f)
                )
            }
            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                KpiTile(
                    label = "本月用量",
                    value = formatTokenCount(data.month.totalTokens),
                    sub = "输出 ${formatTokenCount(data.month.outputTokens)}",
                    modifier = Modifier.weight(1f)
                )
                KpiTile(
                    label = "历史总用量",
                    value = formatTokenCount(data.total.totalTokens),
                    sub = "总请求 ${data.total.requests} 次",
                    highlight = p.accent,
                    modifier = Modifier.weight(1f)
                )
            }
        }
    }
}

@Composable
private fun KpiTile(
    label: String,
    value: String,
    sub: String,
    modifier: Modifier = Modifier,
    highlight: Color? = null
) {
    val p = LocalIosPalette.current
    Column(
        modifier = modifier
            .clip(RoundedCornerShape(10.dp))
            .background(p.secondaryFill.copy(alpha = if (p.isDark) 0.7f else 0.55f))
            .padding(horizontal = 10.dp, vertical = 9.dp)
    ) {
        Text(label, color = p.secondaryLabel, fontSize = 11.sp, maxLines = 1)
        Spacer(Modifier.height(3.dp))
        Text(
            text = value,
            color = highlight ?: p.label,
            fontSize = 18.sp,
            fontWeight = FontWeight.Bold,
            maxLines = 1
        )
        Spacer(Modifier.height(1.dp))
        Text(sub, color = p.secondaryLabel, fontSize = 10.5.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
private fun AgentUsageCard(agent: UsageAgentItem, nowMs: Long) {
    val p = LocalIosPalette.current
    var expanded by remember(agent.agent) { mutableStateOf(false) }
    val agentColor = when (agent.agent.lowercase()) {
        "claude" -> ClaudeColor
        "codex" -> CodexColor
        "agy" -> AgyColor
        else -> p.accent
    }

    IosSection {
        Column(
            modifier = Modifier.fillMaxWidth().padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            // Header
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Box(modifier = Modifier.size(10.dp).clip(CircleShape).background(agentColor))
                    Text(
                        text = agent.displayName,
                        color = p.label,
                        fontSize = 17.sp,
                        fontWeight = FontWeight.Bold
                    )
                    if (agent.planType.isNotBlank()) {
                        IosPillBadge(text = agent.planType.uppercase(), color = agentColor)
                    }
                }
                Text(
                    text = formatRelativeTime(agent.lastActiveAt, nowMs),
                    color = p.secondaryLabel,
                    fontSize = 12.sp
                )
            }

            // 5h & Weekly Quota Windows
            QuotaWindowBox(label = "5 小时滚动窗口", window = agent.window5h, accent = agentColor, nowMs = nowMs)
            QuotaWindowBox(label = "周配额窗口", window = agent.windowWeekly, accent = agentColor, nowMs = nowMs)

            // 4-cell breakdown
            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                MiniStatCell("今日", formatTokenCount(agent.today.totalTokens), Modifier.weight(1f))
                MiniStatCell("本周", formatTokenCount(agent.week.totalTokens), Modifier.weight(1f))
                MiniStatCell("本月", formatTokenCount(agent.month.totalTokens), Modifier.weight(1f))
                MiniStatCell("总计", formatTokenCount(agent.total.totalTokens), Modifier.weight(1f))
            }

            // Expandable per-model breakdown
            if (agent.models.isNotEmpty()) {
                IosDivider(startIndent = 0.dp)
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable { expanded = !expanded }
                        .padding(vertical = 2.dp),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "模型明细（${agent.models.size} 个）· 日志 ${agent.logFiles} 个 (${formatFileSize(agent.logBytes)})",
                        color = p.secondaryLabel,
                        fontSize = 12.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f)
                    )
                    Icon(
                        imageVector = if (expanded) Icons.Default.ExpandLess else Icons.Default.ExpandMore,
                        contentDescription = null,
                        tint = p.secondaryLabel,
                        modifier = Modifier.size(18.dp)
                    )
                }

                if (expanded) {
                    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        Row(modifier = Modifier.fillMaxWidth()) {
                            Text("模型", color = p.tertiaryLabel, fontSize = 11.sp, modifier = Modifier.weight(1.9f))
                            Text("今日", color = p.tertiaryLabel, fontSize = 11.sp, textAlign = TextAlign.End, modifier = Modifier.weight(0.8f))
                            Text("本周", color = p.tertiaryLabel, fontSize = 11.sp, textAlign = TextAlign.End, modifier = Modifier.weight(0.8f))
                            Text("总计", color = p.tertiaryLabel, fontSize = 11.sp, textAlign = TextAlign.End, modifier = Modifier.weight(0.9f))
                        }
                        agent.models.take(6).forEach { m ->
                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                Text(
                                    text = m.model,
                                    color = p.label,
                                    fontSize = 12.sp,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.weight(1.9f)
                                )
                                Text(
                                    text = formatTokenCount(m.today.totalTokens),
                                    color = p.secondaryLabel,
                                    fontSize = 12.sp,
                                    textAlign = TextAlign.End,
                                    modifier = Modifier.weight(0.8f)
                                )
                                Text(
                                    text = formatTokenCount(m.week.totalTokens),
                                    color = p.secondaryLabel,
                                    fontSize = 12.sp,
                                    textAlign = TextAlign.End,
                                    modifier = Modifier.weight(0.8f)
                                )
                                Text(
                                    text = formatTokenCount(m.total.totalTokens),
                                    color = p.label,
                                    fontSize = 12.sp,
                                    fontWeight = FontWeight.SemiBold,
                                    textAlign = TextAlign.End,
                                    modifier = Modifier.weight(0.9f)
                                )
                            }
                        }
                    }
                }
            }

            if (agent.historyNote.isNotBlank()) {
                Text(
                    text = agent.historyNote,
                    color = p.tertiaryLabel,
                    fontSize = 11.sp,
                    lineHeight = 15.sp,
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(p.secondaryFill.copy(alpha = if (p.isDark) 0.45f else 0.4f))
                        .padding(horizontal = 10.dp, vertical = 7.dp)
                )
            }
        }
    }
}

@Composable
private fun QuotaWindowBox(
    label: String,
    window: UsageQuotaWindow,
    accent: Color,
    nowMs: Long
) {
    val p = LocalIosPalette.current
    val pct = window.usedPercent
    val statusColor = when {
        pct == null -> accent
        pct >= 85.0 -> p.red
        pct >= 60.0 -> p.orange
        else -> p.green
    }
    val rightText = when {
        window.source == "official" && window.expired -> "已重置 (0%)"
        pct != null -> {
            val left = (100.0 - pct).coerceAtLeast(0.0)
            String.format(Locale.US, "已用 %.0f%% · 剩 %.0f%%", pct, left)
        }
        else -> "${formatTokenCount(window.tokens.totalTokens)} tok"
    }
    val fraction = when {
        pct != null -> (pct / 100.0).toFloat().coerceIn(0f, 1f)
        window.tokens.totalTokens > 0L -> 1f
        else -> 0f
    }
    val resetLine = when {
        window.resetsAt > 0L -> "${formatResetClock(window.resetsAt, nowMs)} 重置 · ${formatCountdown(window.resetsAt, nowMs)}"
        window.source == "official" -> "配额满额可用"
        window.source == "local" && window.tokens.totalTokens > 0L -> "仅本地消耗统计"
        else -> "当前窗口空闲"
    }

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(p.secondaryFill.copy(alpha = if (p.isDark) 0.65f else 0.55f))
            .padding(horizontal = 12.dp, vertical = 9.dp),
        verticalArrangement = Arrangement.spacedBy(5.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(label, color = p.label, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
            Text(rightText, color = statusColor, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        }
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(6.dp)
                .clip(CircleShape)
                .background(p.separator.copy(alpha = 0.45f))
        ) {
            if (fraction > 0f) {
                Box(
                    modifier = Modifier
                        .fillMaxWidth(fraction.coerceAtLeast(0.02f))
                        .fillMaxHeight()
                        .clip(CircleShape)
                        .background(statusColor.copy(alpha = if (pct != null) 1f else 0.45f))
                )
            }
        }
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Text(resetLine, color = p.secondaryLabel, fontSize = 11.5.sp)
            if (window.tokens.requests > 0) {
                Text("${window.tokens.requests} 次", color = p.secondaryLabel, fontSize = 11.5.sp)
            }
        }
        if (window.note.isNotBlank()) {
            Text(window.note, color = p.tertiaryLabel, fontSize = 11.sp, lineHeight = 14.sp)
        }
    }
}

@Composable
private fun MiniStatCell(label: String, value: String, modifier: Modifier = Modifier) {
    val p = LocalIosPalette.current
    Column(
        modifier = modifier
            .clip(RoundedCornerShape(8.dp))
            .background(p.secondaryFill.copy(alpha = if (p.isDark) 0.5f else 0.45f))
            .padding(vertical = 7.dp, horizontal = 4.dp),
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        Text(label, color = p.secondaryLabel, fontSize = 10.5.sp)
        Spacer(Modifier.height(2.dp))
        Text(value, color = p.label, fontSize = 13.sp, fontWeight = FontWeight.Bold, maxLines = 1)
    }
}

@Composable
private fun DailyChartSection(
    daily: List<UsageDailyPoint>,
    selectedIndex: Int?,
    onSelect: (Int) -> Unit
) {
    val p = LocalIosPalette.current
    val maxTotal = (daily.maxOfOrNull { it.total } ?: 1L).coerceAtLeast(1L)

    IosSection(title = "近 14 天用量趋势（点击柱子看明细）") {
        Column(
            modifier = Modifier.fillMaxWidth().padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            // Legend
            Row(horizontalArrangement = Arrangement.spacedBy(14.dp), verticalAlignment = Alignment.CenterVertically) {
                LegendDot("Claude", ClaudeColor)
                LegendDot("Codex", CodexColor)
                LegendDot("Antigravity", AgyColor)
            }

            // Bars
            Row(
                modifier = Modifier.fillMaxWidth().height(126.dp),
                horizontalArrangement = Arrangement.spacedBy(4.dp),
                verticalAlignment = Alignment.Bottom
            ) {
                daily.forEachIndexed { idx, d ->
                    val hFrac = (d.total.toFloat() / maxTotal.toFloat()).coerceIn(0f, 1f)
                    val isSel = selectedIndex == idx
                    Column(
                        modifier = Modifier
                            .weight(1f)
                            .fillMaxHeight()
                            .clickable { onSelect(idx) },
                        horizontalAlignment = Alignment.CenterHorizontally,
                        verticalArrangement = Arrangement.Bottom
                    ) {
                        Box(
                            modifier = Modifier
                                .fillMaxWidth(0.76f)
                                .height((94f * hFrac).dp.coerceAtLeast(3.dp))
                                .clip(RoundedCornerShape(topStart = 4.dp, topEnd = 4.dp))
                                .background(p.secondaryFill)
                        ) {
                            if (d.total > 0L) {
                                Column(
                                    modifier = Modifier.fillMaxSize(),
                                    verticalArrangement = Arrangement.Bottom
                                ) {
                                    if (d.agy > 0L) {
                                        Box(
                                            modifier = Modifier
                                                .fillMaxWidth()
                                                .weight(d.agy.toFloat())
                                                .background(AgyColor.copy(alpha = if (isSel) 1f else 0.85f))
                                        )
                                    }
                                    if (d.codex > 0L) {
                                        Box(
                                            modifier = Modifier
                                                .fillMaxWidth()
                                                .weight(d.codex.toFloat())
                                                .background(CodexColor.copy(alpha = if (isSel) 1f else 0.85f))
                                        )
                                    }
                                    if (d.claude > 0L) {
                                        Box(
                                            modifier = Modifier
                                                .fillMaxWidth()
                                                .weight(d.claude.toFloat())
                                                .background(ClaudeColor.copy(alpha = if (isSel) 1f else 0.85f))
                                        )
                                    }
                                }
                            }
                        }
                        Spacer(Modifier.height(4.dp))
                        Text(
                            text = if (idx % 2 == 1 || isSel) d.date.takeLast(2) else "",
                            color = if (isSel) p.accent else p.tertiaryLabel,
                            fontSize = 9.5.sp,
                            fontWeight = if (isSel) FontWeight.Bold else FontWeight.Normal,
                            maxLines = 1
                        )
                    }
                }
            }

            // Selected Day Detail Strip
            val sel = selectedIndex?.let { daily.getOrNull(it) }
            if (sel != null) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(p.secondaryFill.copy(alpha = 0.6f))
                        .padding(horizontal = 10.dp, vertical = 7.dp),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text("${sel.date} · 总 ${formatTokenCount(sel.total)}", color = p.label, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
                    Text(
                        text = "C:${formatTokenCount(sel.claude)}  X:${formatTokenCount(sel.codex)}  A:${formatTokenCount(sel.agy)}",
                        color = p.secondaryLabel,
                        fontSize = 11.5.sp
                    )
                }
            }
        }
    }
}

@Composable
private fun LegendDot(name: String, color: Color) {
    val p = LocalIosPalette.current
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
        Box(modifier = Modifier.size(8.dp).clip(CircleShape).background(color))
        Text(name, color = p.secondaryLabel, fontSize = 11.5.sp)
    }
}

private fun formatResetClock(ms: Long, nowMs: Long): String {
    if (ms <= 0L) return "--"
    val target = Calendar.getInstance().apply { timeInMillis = ms }
    val now = Calendar.getInstance().apply { timeInMillis = nowMs }
    val sameDay = target.get(Calendar.YEAR) == now.get(Calendar.YEAR) &&
        target.get(Calendar.DAY_OF_YEAR) == now.get(Calendar.DAY_OF_YEAR)
    if (sameDay) {
        return SimpleDateFormat("HH:mm", Locale.CHINA).format(Date(ms))
    }
    val wk = arrayOf("", "周日", "周一", "周二", "周三", "周四", "周五", "周六")[target.get(Calendar.DAY_OF_WEEK)]
    return SimpleDateFormat("M/d", Locale.CHINA).format(Date(ms)) + "($wk) " +
        SimpleDateFormat("HH:mm", Locale.CHINA).format(Date(ms))
}

private fun formatClockTime(ms: Long): String {
    if (ms <= 0L) return "--"
    return SimpleDateFormat("HH:mm:ss", Locale.CHINA).format(Date(ms))
}

private fun formatCountdown(targetMs: Long, nowMs: Long): String {
    if (targetMs <= 0L) return "无倒计时"
    val diffSec = (targetMs - nowMs) / 1000L
    if (diffSec <= 0L) return "已重置"
    val days = diffSec / 86400L
    val hrs = (diffSec % 86400L) / 3600L
    val mins = (diffSec % 3600L) / 60L
    val secs = diffSec % 60L
    return when {
        days > 0L -> "剩 ${days}天 ${hrs}h ${mins}m"
        hrs > 0L -> "剩 ${hrs}h ${mins}m ${secs}s"
        else -> "剩 ${mins}m ${secs}s"
    }
}

private fun formatRelativeTime(tsMs: Long, nowMs: Long): String {
    if (tsMs <= 0L) return "暂无记录"
    val diffSec = ((nowMs - tsMs) / 1000L).coerceAtLeast(0L)
    return when {
        diffSec < 60L -> "刚刚活跃"
        diffSec < 3600L -> "${diffSec / 60L} 分钟前"
        diffSec < 86400L -> "${diffSec / 3600L} 小时前"
        else -> "${diffSec / 86400L} 天前"
    }
}
