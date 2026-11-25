package com.phoneaiaudit.app.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.phoneaiaudit.app.data.AuditRepository

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED ||
            intent.action == Intent.ACTION_MY_PACKAGE_REPLACED
        ) {
            AuditRepository.init(context)
            if (AuditRepository.config.isConfigured()) {
                AuditForegroundService.start(context)
            }
        }
    }
}
