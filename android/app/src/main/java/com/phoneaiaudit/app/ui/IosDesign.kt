package com.phoneaiaudit.app.ui

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

data class IosPalette(
    val isDark: Boolean,
    val bg: Color,
    val card: Color,
    val secondaryFill: Color,
    val segmentSelected: Color,
    val separator: Color,
    val label: Color,
    val secondaryLabel: Color,
    val tertiaryLabel: Color,
    val accent: Color,
    val green: Color,
    val orange: Color,
    val red: Color,
    val codeBg: Color,
    val codeFg: Color,
    val tabBarBg: Color,
    val switchOffTrack: Color
)

val LightIosPalette = IosPalette(
    isDark = false,
    bg = Color(0xFFF2F2F7),
    card = Color(0xFFFFFFFF),
    secondaryFill = Color(0xFFE5E5EA),
    segmentSelected = Color(0xFFFFFFFF),
    separator = Color(0xFFD1D1D6),
    label = Color(0xFF000000),
    secondaryLabel = Color(0xFF6C6C70),
    tertiaryLabel = Color(0xFF8E8E93),
    accent = Color(0xFF007AFF),
    green = Color(0xFF34C759),
    orange = Color(0xFFFF9500),
    red = Color(0xFFFF3B30),
    codeBg = Color(0xFFF4F5F7),
    codeFg = Color(0xFF1C1C1E),
    tabBarBg = Color(0xFFF9F9FB),
    switchOffTrack = Color(0xFFE9E9EA)
)

val DarkIosPalette = IosPalette(
    isDark = true,
    bg = Color(0xFF000000),
    card = Color(0xFF1C1C1E),
    secondaryFill = Color(0xFF2C2C2E),
    segmentSelected = Color(0xFF636366),
    separator = Color(0xFF38383A),
    label = Color(0xFFFFFFFF),
    secondaryLabel = Color(0xFF98989F),
    tertiaryLabel = Color(0xFF636366),
    accent = Color(0xFF0A84FF),
    green = Color(0xFF30D158),
    orange = Color(0xFFFF9F0A),
    red = Color(0xFFFF453A),
    codeBg = Color(0xFF111214),
    codeFg = Color(0xFFE5E5EA),
    tabBarBg = Color(0xFF121214),
    switchOffTrack = Color(0xFF39393D)
)

val LocalIosPalette = staticCompositionLocalOf { DarkIosPalette }

@Composable
fun ProvideIosPalette(isDark: Boolean, content: @Composable () -> Unit) {
    val palette = if (isDark) DarkIosPalette else LightIosPalette
    CompositionLocalProvider(LocalIosPalette provides palette, content = content)
}

@Composable
fun IosPageHeader(
    title: String,
    subtitle: String? = null,
    trailing: (@Composable RowScope.() -> Unit)? = null
) {
    val p = LocalIosPalette.current
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .statusBarsPadding()
            .padding(start = 16.dp, end = 16.dp, top = 12.dp, bottom = 8.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = title,
                color = p.label,
                fontSize = 28.sp,
                fontWeight = FontWeight.Bold,
                letterSpacing = (-0.4).sp
            )
            if (!subtitle.isNullOrBlank()) {
                Spacer(Modifier.height(2.dp))
                Text(
                    text = subtitle,
                    color = p.secondaryLabel,
                    fontSize = 13.sp,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }
        }
        if (trailing != null) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                content = trailing
            )
        }
    }
}

@Composable
fun IosSection(
    title: String? = null,
    footer: String? = null,
    modifier: Modifier = Modifier,
    content: @Composable ColumnScope.() -> Unit
) {
    val p = LocalIosPalette.current
    Column(modifier = modifier.fillMaxWidth()) {
        if (!title.isNullOrBlank()) {
            Text(
                text = title,
                color = p.secondaryLabel,
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium,
                modifier = Modifier.padding(start = 16.dp, bottom = 6.dp)
            )
        }
        Surface(
            modifier = Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(14.dp),
            color = p.card,
            border = if (p.isDark) null else BorderStroke(0.5.dp, p.separator.copy(alpha = 0.55f))
        ) {
            Column(modifier = Modifier.fillMaxWidth(), content = content)
        }
        if (!footer.isNullOrBlank()) {
            Text(
                text = footer,
                color = p.secondaryLabel,
                fontSize = 12.sp,
                lineHeight = 17.sp,
                modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 6.dp)
            )
        }
    }
}

@Composable
fun IosDivider(startIndent: Dp = 16.dp) {
    val p = LocalIosPalette.current
    HorizontalDivider(
        modifier = Modifier.padding(start = startIndent),
        thickness = 0.5.dp,
        color = p.separator.copy(alpha = 0.65f)
    )
}

@Composable
fun IosSwitch(
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    enabled: Boolean = true
) {
    val p = LocalIosPalette.current
    val trackColor by animateColorAsState(
        targetValue = if (checked) p.green else p.switchOffTrack,
        animationSpec = tween(180),
        label = "ios_switch_track"
    )
    val thumbOffset by animateDpAsState(
        targetValue = if (checked) 22.dp else 2.dp,
        animationSpec = tween(180),
        label = "ios_switch_thumb"
    )
    Box(
        modifier = Modifier
            .size(width = 50.dp, height = 30.dp)
            .clip(CircleShape)
            .background(if (enabled) trackColor else trackColor.copy(alpha = 0.5f))
            .clickable(
                enabled = enabled,
                interactionSource = remember { MutableInteractionSource() },
                indication = null
            ) { onCheckedChange(!checked) },
        contentAlignment = Alignment.CenterStart
    ) {
        Box(
            modifier = Modifier
                .offset(x = thumbOffset)
                .size(26.dp)
                .shadow(2.dp, CircleShape)
                .clip(CircleShape)
                .background(Color.White)
        )
    }
}

@Composable
fun IosSwitchRow(
    title: String,
    subtitle: String? = null,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit
) {
    val p = LocalIosPalette.current
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onCheckedChange(!checked) }
            .padding(horizontal = 16.dp, vertical = 12.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(modifier = Modifier.weight(1f).padding(end = 12.dp)) {
            Text(
                text = title,
                color = p.label,
                fontSize = 15.sp,
                fontWeight = FontWeight.Normal
            )
            if (!subtitle.isNullOrBlank()) {
                Spacer(Modifier.height(2.dp))
                Text(
                    text = subtitle,
                    color = p.secondaryLabel,
                    fontSize = 12.sp,
                    lineHeight = 16.sp
                )
            }
        }
        IosSwitch(checked = checked, onCheckedChange = onCheckedChange)
    }
}

@Composable
fun IosSegmentedControl(
    options: List<Pair<String, String>>,
    selectedKey: String,
    onSelect: (String) -> Unit,
    modifier: Modifier = Modifier
) {
    val p = LocalIosPalette.current
    Row(
        modifier = modifier
            .fillMaxWidth()
            .height(36.dp)
            .clip(RoundedCornerShape(10.dp))
            .background(p.secondaryFill)
            .padding(2.5.dp),
        horizontalArrangement = Arrangement.spacedBy(2.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        options.forEach { (key, label) ->
            val selected = key == selectedKey
            Box(
                modifier = Modifier
                    .weight(1f)
                    .height(31.dp)
                    .then(
                        if (selected) Modifier
                            .shadow(if (p.isDark) 0.dp else 1.5.dp, RoundedCornerShape(8.dp))
                            .clip(RoundedCornerShape(8.dp))
                            .background(p.segmentSelected)
                        else Modifier.clip(RoundedCornerShape(8.dp))
                    )
                    .clickable(
                        interactionSource = remember { MutableInteractionSource() },
                        indication = null
                    ) { onSelect(key) },
                contentAlignment = Alignment.Center
            ) {
                Text(
                    text = label,
                    color = if (selected) p.label else p.secondaryLabel,
                    fontSize = 13.sp,
                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }
        }
    }
}

@Composable
fun IosButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    containerColor: Color = LocalIosPalette.current.accent,
    contentColor: Color = Color.White,
    enabled: Boolean = true,
    icon: ImageVector? = null
) {
    Button(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.height(44.dp),
        shape = RoundedCornerShape(12.dp),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = containerColor,
            contentColor = contentColor,
            disabledContainerColor = containerColor.copy(alpha = 0.35f),
            disabledContentColor = contentColor.copy(alpha = 0.6f)
        ),
        elevation = ButtonDefaults.buttonElevation(0.dp, 0.dp, 0.dp)
    ) {
        if (icon != null) {
            Icon(icon, contentDescription = null, modifier = Modifier.size(16.dp))
            if (text.isNotEmpty()) {
                Spacer(Modifier.width(5.dp))
            }
        }
        if (text.isNotEmpty()) {
            Text(
                text = text,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }
    }
}

@Composable
fun IosTintedButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    tint: Color = LocalIosPalette.current.accent,
    enabled: Boolean = true,
    icon: ImageVector? = null
) {
    val p = LocalIosPalette.current
    IosButton(
        text = text,
        onClick = onClick,
        modifier = modifier,
        containerColor = tint.copy(alpha = if (p.isDark) 0.18f else 0.12f),
        contentColor = tint,
        enabled = enabled,
        icon = icon
    )
}

data class IosTabItem(
    val label: String,
    val icon: ImageVector,
    val badgeCount: Int = 0
)

@Composable
fun IosBottomBar(
    items: List<IosTabItem>,
    selectedIndex: Int,
    onSelect: (Int) -> Unit
) {
    val p = LocalIosPalette.current
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(p.tabBarBg)
            .navigationBarsPadding()
    ) {
        HorizontalDivider(thickness = 0.5.dp, color = p.separator.copy(alpha = 0.7f))
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(54.dp)
                .padding(horizontal = 4.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            items.forEachIndexed { idx, item ->
                val selected = idx == selectedIndex
                val color = if (selected) p.accent else p.secondaryLabel
                Box(
                    modifier = Modifier
                        .weight(1f)
                        .height(54.dp)
                        .clickable(
                            interactionSource = remember { MutableInteractionSource() },
                            indication = null
                        ) { onSelect(idx) },
                    contentAlignment = Alignment.Center
                ) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        Box {
                            Icon(
                                imageVector = item.icon,
                                contentDescription = item.label,
                                tint = color,
                                modifier = Modifier.size(22.dp)
                            )
                            if (item.badgeCount > 0) {
                                Box(
                                    modifier = Modifier
                                        .align(Alignment.TopEnd)
                                        .offset(x = 8.dp, y = (-4).dp)
                                        .clip(CircleShape)
                                        .background(p.red)
                                        .padding(horizontal = 4.5.dp, vertical = 0.5.dp),
                                    contentAlignment = Alignment.Center
                                ) {
                                    Text(
                                        text = if (item.badgeCount > 99) "99+" else "${item.badgeCount}",
                                        color = Color.White,
                                        fontSize = 10.sp,
                                        fontWeight = FontWeight.Bold,
                                        lineHeight = 12.sp
                                    )
                                }
                            }
                        }
                        Spacer(Modifier.height(2.dp))
                        Text(
                            text = item.label,
                            color = color,
                            fontSize = 10.sp,
                            fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium
                        )
                    }
                }
            }
        }
    }
}

@Composable
fun IosPillBadge(text: String, color: Color) {
    Box(
        modifier = Modifier
            .clip(RoundedCornerShape(6.dp))
            .background(color.copy(alpha = 0.16f))
            .border(0.5.dp, color.copy(alpha = 0.35f), RoundedCornerShape(6.dp))
            .padding(horizontal = 7.dp, vertical = 2.dp)
    ) {
        Text(
            text = text,
            color = color,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
            maxLines = 1
        )
    }
}
