package com.phoneaiaudit.app.notification

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.RemoteInput
import com.phoneaiaudit.app.MainActivity
import com.phoneaiaudit.app.R
import com.phoneaiaudit.app.data.ApprovalItem
import com.phoneaiaudit.app.data.AuditMessage
import com.phoneaiaudit.app.data.agentDisplayName

object AuditNotifier {
    private const val LEGACY_CHANNEL_SERVICE = "audit_service_channel"
    const val CHANNEL_SERVICE = "audit_service_silent_v2"
    const val CHANNEL_APPROVAL = "audit_approval_channel"
    const val CHANNEL_MESSAGE = "audit_message_channel"

    const val FOREGROUND_NOTIFICATION_ID = 1001
    const val KEY_DENY_REASON = "key_deny_reason"

    fun ensureChannels(context: Context) {
        val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

        // Delete old IMPORTANCE_LOW service channel so Android applies the new silent MIN/SECRET settings
        try {
            nm.deleteNotificationChannel(LEGACY_CHANNEL_SERVICE)
        } catch (_: Exception) {
        }

        val serviceCh = NotificationChannel(
            CHANNEL_SERVICE,
            "后台保活（可关闭此项以彻底隐藏）",
            NotificationManager.IMPORTANCE_MIN
        ).apply {
            description = "仅用于后台保活，关闭此通知类别不会影响审批和消息推送"
            setShowBadge(false)
            enableLights(false)
            enableVibration(false)
            setSound(null, null)
            lockscreenVisibility = Notification.VISIBILITY_SECRET
        }

        val approvalCh = NotificationChannel(
            CHANNEL_APPROVAL,
            "AI 审批与紧急提醒",
            NotificationManager.IMPORTANCE_HIGH
        ).apply {
            description = "Claude Code / Codex / Antigravity 权限审批与异常提醒"
            enableVibration(true)
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
        }

        val messageCh = NotificationChannel(
            CHANNEL_MESSAGE,
            "AI 任务完成通知",
            NotificationManager.IMPORTANCE_DEFAULT
        ).apply {
            description = "任务完成与常规状态通知"
        }

        nm.createNotificationChannel(serviceCh)
        nm.createNotificationChannel(approvalCh)
        nm.createNotificationChannel(messageCh)
    }

    fun buildForegroundNotification(context: Context, statusText: String): Notification {
        val openIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val pi = PendingIntent.getActivity(
            context, 0, openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        return NotificationCompat.Builder(context, CHANNEL_SERVICE)
            .setSmallIcon(R.drawable.ic_notification)
            .setColor(0xFF1C212C.toInt())
            .setContentTitle("AI Audit")
            .setContentText(statusText)
            .setOngoing(true)
            .setSilent(true)
            .setShowWhen(false)
            .setOnlyAlertOnce(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .setVisibility(NotificationCompat.VISIBILITY_SECRET)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_DEFERRED)
            .setContentIntent(pi)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .build()
    }

    fun showApproval(
        context: Context,
        item: ApprovalItem,
        highRiskRequireApp: Boolean,
        autoApproveOnTimeout: Boolean = true
    ) {
        val notifId = approvalNotificationId(item.id)
        val openIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
            putExtra("open_tab", 0)
            putExtra("approval_id", item.id)
        }
        val openPi = PendingIntent.getActivity(
            context, notifId, openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val nowMs = System.currentTimeMillis()
        val leftMs = if (item.expiresAt > nowMs) item.expiresAt - nowMs else 0L
        val leftSec = if (leftMs > 0L) ((leftMs + 999L) / 1000L) else 0L

        val riskTag = if (item.isHighRisk) "【高危】" else ""
        val title = "$riskTag${agentDisplayName(item.agent)} 请求审批"
        val bigText = buildString {
            if (item.projectName.isNotBlank()) append("项目: ${item.projectName}\n")
            if (item.taskTitle.isNotBlank()) append("任务: ${item.taskTitle}\n")
            append(item.summary)
            if (item.riskReasons.isNotEmpty()) {
                append("\n风险: ${item.riskReasons.joinToString("；")}")
            }
            if (autoApproveOnTimeout && leftSec > 0L) {
                append("\n超时提示: ${leftSec}s 内未操作将自动允许")
            }
        }

        val builder = NotificationCompat.Builder(context, CHANNEL_APPROVAL)
            .setSmallIcon(R.drawable.ic_notification)
            .setColor(0xFF30D158.toInt())
            .setContentTitle(title)
            .setContentText(item.summary)
            .setStyle(NotificationCompat.BigTextStyle().bigText(bigText))
            .setPriority(NotificationCompat.PRIORITY_MAX)
            .setCategory(NotificationCompat.CATEGORY_ALARM)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setAutoCancel(false)
            .setContentIntent(openPi)
        if (autoApproveOnTimeout && leftSec > 0L) {
            builder.setSubText("${leftSec}s 自动允许")
        }
        if (leftMs > 0L) {
            builder.setTimeoutAfter(leftMs + 1500L)
        }

        // Allow action: runs silently in background via BroadcastReceiver without opening the App UI
        if (!item.isHighRisk || !highRiskRequireApp) {
            val allowIntent = Intent(context, ApprovalActionReceiver::class.java).apply {
                setPackage(context.packageName)
                action = ApprovalActionReceiver.ACTION_APPROVE
                putExtra(ApprovalActionReceiver.EXTRA_APPROVAL_ID, item.id)
            }
            val allowPi = PendingIntent.getBroadcast(
                context, notifId * 10 + 1, allowIntent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            val allowAction = NotificationCompat.Action.Builder(0, "允许", allowPi)
                .setShowsUserInterface(false)
                .setAuthenticationRequired(false)
                .build()
            builder.addAction(allowAction)
        } else {
            builder.addAction(0, "打开确认", openPi)
        }

        // Deny action with inline RemoteInput reason (also runs in background without opening App)
        val remoteInput = RemoteInput.Builder(KEY_DENY_REASON)
            .setLabel("可选：输入拒绝理由给 AI")
            .build()
        val denyIntent = Intent(context, ApprovalActionReceiver::class.java).apply {
            setPackage(context.packageName)
            action = ApprovalActionReceiver.ACTION_DENY
            putExtra(ApprovalActionReceiver.EXTRA_APPROVAL_ID, item.id)
        }
        val denyFlags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
        } else {
            PendingIntent.FLAG_UPDATE_CURRENT
        }
        val denyPi = PendingIntent.getBroadcast(context, notifId * 10 + 2, denyIntent, denyFlags)
        val denyAction = NotificationCompat.Action.Builder(0, "拒绝", denyPi)
            .addRemoteInput(remoteInput)
            .setShowsUserInterface(false)
            .setAuthenticationRequired(false)
            .build()
        builder.addAction(denyAction)

        // Fallback to Mac action (runs in background without opening App)
        val fbIntent = Intent(context, ApprovalActionReceiver::class.java).apply {
            setPackage(context.packageName)
            action = ApprovalActionReceiver.ACTION_FALLBACK
            putExtra(ApprovalActionReceiver.EXTRA_APPROVAL_ID, item.id)
        }
        val fbPi = PendingIntent.getBroadcast(
            context, notifId * 10 + 3, fbIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val fbAction = NotificationCompat.Action.Builder(0, "退回电脑", fbPi)
            .setShowsUserInterface(false)
            .setAuthenticationRequired(false)
            .build()
        builder.addAction(fbAction)

        try {
            NotificationManagerCompat.from(context).notify(notifId, builder.build())
        } catch (_: SecurityException) {
        }
    }

    fun cancelApproval(context: Context, approvalId: String) {
        NotificationManagerCompat.from(context).cancel(approvalNotificationId(approvalId))
    }

    fun cancelMessage(context: Context, messageId: String) {
        NotificationManagerCompat.from(context).cancel(messageNotificationId(messageId))
    }

    fun showMessage(context: Context, msg: AuditMessage) {
        if (msg.eventType == "approval_result") return
        val notifId = messageNotificationId(msg.id)
        val channel = if (msg.priority == "high" || msg.eventType == "error" || msg.eventType == "attention") {
            CHANNEL_APPROVAL
        } else {
            CHANNEL_MESSAGE
        }
        val openIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
            putExtra("open_tab", 0)
            putExtra("message_id", msg.id)
            if (msg.tmuxSession.isNotBlank()) {
                putExtra("tmux_session", msg.tmuxSession)
            }
        }
        val pi = PendingIntent.getActivity(
            context, notifId, openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val detail = buildString {
            if (msg.projectName.isNotBlank()) append("[${msg.projectName}] ")
            append(msg.body.ifBlank { msg.taskTitle })
        }

        val builder = NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_notification)
            .setColor(0xFF30D158.toInt())
            .setContentTitle(msg.title)
            .setContentText(detail)
            .setStyle(NotificationCompat.BigTextStyle().bigText(detail))
            .setAutoCancel(true)
            .setContentIntent(pi)

        if (msg.tmuxSession.isNotBlank()) {
            val termIntent = Intent(context, MainActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
                putExtra("open_terminal_tmux", msg.tmuxSession)
                putExtra("message_id", msg.id)
            }
            val termPi = PendingIntent.getActivity(
                context, notifId + 1, termIntent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            builder.addAction(0, "进入终端", termPi)
        }

        try {
            NotificationManagerCompat.from(context).notify(notifId, builder.build())
        } catch (_: SecurityException) {
        }
    }

    private fun approvalNotificationId(id: String): Int = (id.hashCode() and 0x00FFFFFF) + 5000
    private fun messageNotificationId(id: String): Int = (id.hashCode() and 0x00FFFFFF) + 20000
}
