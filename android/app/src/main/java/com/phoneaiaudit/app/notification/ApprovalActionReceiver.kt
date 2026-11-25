package com.phoneaiaudit.app.notification

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.widget.Toast
import androidx.core.app.RemoteInput
import com.phoneaiaudit.app.BuildConfig
import com.phoneaiaudit.app.data.AuditRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

class ApprovalActionReceiver : BroadcastReceiver() {
    companion object {
        val ACTION_APPROVE = "${BuildConfig.APPLICATION_ID}.ACTION_APPROVE"
        val ACTION_DENY = "${BuildConfig.APPLICATION_ID}.ACTION_DENY"
        val ACTION_FALLBACK = "${BuildConfig.APPLICATION_ID}.ACTION_FALLBACK"
        const val EXTRA_APPROVAL_ID = "approval_id"
    }

    override fun onReceive(context: Context, intent: Intent) {
        val appCtx = context.applicationContext
        AuditRepository.init(appCtx)
        val approvalId = intent.getStringExtra(EXTRA_APPROVAL_ID) ?: return
        val decision = when (intent.action) {
            ACTION_APPROVE -> "approved"
            ACTION_DENY -> "denied"
            ACTION_FALLBACK -> "fallback"
            else -> return
        }
        val reason = RemoteInput.getResultsFromIntent(intent)
            ?.getCharSequence(AuditNotifier.KEY_DENY_REASON)
            ?.toString()
            ?.trim()
            .orEmpty()

        val label = when (decision) {
            "approved" -> "已允许"
            "denied" -> "已拒绝"
            else -> "已退回电脑"
        }

        // Immediately dismiss notification on tap to prevent accidental double-tap opening the App
        AuditNotifier.cancelApproval(appCtx, approvalId)

        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                val res = AuditRepository.decideApproval(approvalId, decision, reason)
                val toastText = if (res.isSuccess) "审批$label" else "审批提交失败: ${res.exceptionOrNull()?.message}"
                Handler(Looper.getMainLooper()).post {
                    Toast.makeText(appCtx, toastText, Toast.LENGTH_SHORT).show()
                }
            } finally {
                pending.finish()
            }
        }
    }
}
