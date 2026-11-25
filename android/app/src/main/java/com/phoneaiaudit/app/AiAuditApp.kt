package com.phoneaiaudit.app

import android.app.Application
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.notification.AuditNotifier
import com.phoneaiaudit.app.service.AuditForegroundService

class AiAuditApp : Application() {
    override fun onCreate() {
        super.onCreate()
        AuditRepository.init(this)
        AuditNotifier.ensureChannels(this)
        if (AuditRepository.config.isConfigured()) {
            try {
                AuditForegroundService.start(this)
            } catch (_: Exception) {
            }
        }
    }
}
