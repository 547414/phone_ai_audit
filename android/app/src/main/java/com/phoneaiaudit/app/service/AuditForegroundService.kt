package com.phoneaiaudit.app.service

import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.os.IBinder
import com.phoneaiaudit.app.BuildConfig
import com.phoneaiaudit.app.data.ApprovalItem
import com.phoneaiaudit.app.data.AuditMessage
import com.phoneaiaudit.app.data.AuditRepository
import com.phoneaiaudit.app.data.ConnectionState
import com.phoneaiaudit.app.notification.AuditNotifier
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject

class AuditForegroundService : Service() {
    companion object {
        val ACTION_RECONNECT = "${BuildConfig.APPLICATION_ID}.RECONNECT"

        fun start(context: Context, reconnect: Boolean = false) {
            val intent = Intent(context, AuditForegroundService::class.java).apply {
                if (reconnect) action = ACTION_RECONNECT
            }
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, AuditForegroundService::class.java))
        }
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var webSocket: WebSocket? = null
    private var reconnectJob: Job? = null
    private var pingJob: Job? = null
    private var backoffMs = 1500L
    private var netCallback: ConnectivityManager.NetworkCallback? = null

    override fun onCreate() {
        super.onCreate()
        AuditRepository.init(this)
        AuditNotifier.ensureChannels(this)
        val notif = AuditNotifier.buildForegroundNotification(this, "后台监听运行中")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                AuditNotifier.FOREGROUND_NOTIFICATION_ID,
                notif,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            )
        } else {
            startForeground(AuditNotifier.FOREGROUND_NOTIFICATION_ID, notif)
        }
        registerNetworkMonitor()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!AuditRepository.config.isConfigured()) {
            updateForegroundText("未配置服务器，请打开 App 扫码配对")
            AuditRepository.setConnectionState(ConnectionState.DISCONNECTED)
            return START_STICKY
        }
        if (intent?.action == ACTION_RECONNECT || webSocket == null) {
            backoffMs = 1500L
            connectNow()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        unregisterNetworkMonitor()
        webSocket?.close(1000, "service_stopped")
        webSocket = null
        AuditRepository.setConnectionState(ConnectionState.DISCONNECTED)
        scope.cancel()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun registerNetworkMonitor() {
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                if (AuditRepository.config.isConfigured() &&
                    AuditRepository.connectionState.value != ConnectionState.CONNECTED
                ) {
                    backoffMs = 1000L
                    connectNow()
                }
            }
        }
        netCallback = cb
        try {
            cm.registerDefaultNetworkCallback(cb)
        } catch (_: Exception) {
        }
    }

    private fun unregisterNetworkMonitor() {
        val cb = netCallback ?: return
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        try {
            cm.unregisterNetworkCallback(cb)
        } catch (_: Exception) {
        }
        netCallback = null
    }

    @Synchronized
    private fun connectNow() {
        reconnectJob?.cancel()
        pingJob?.cancel()
        webSocket?.cancel()
        webSocket = null

        val cfg = AuditRepository.config
        if (!cfg.isConfigured()) return

        AuditRepository.setConnectionState(ConnectionState.CONNECTING)
        updateForegroundText("正在连接 ${cfg.serverUrl} ...")

        val req = Request.Builder()
            .url("${cfg.wsBaseUrl()}/api/v1/ws/app")
            .header("Authorization", "Bearer ${cfg.appToken}")
            .build()

        webSocket = AuditRepository.httpClient.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(ws: WebSocket, response: Response) {
                backoffMs = 1500L
                AuditRepository.setConnectionState(ConnectionState.CONNECTED)
                updateForegroundText("在线 · 实时监听 Claude / Codex / AGY")
                startAppPing(ws)
                scope.launch { AuditRepository.refreshAll() }
            }

            override fun onMessage(ws: WebSocket, text: String) {
                handleWsMessage(text)
            }

            override fun onClosing(ws: WebSocket, code: Int, reason: String) {
                ws.close(1000, null)
            }

            override fun onClosed(ws: WebSocket, code: Int, reason: String) {
                if (webSocket == ws) {
                    webSocket = null
                    scheduleReconnect("连接已断开 ($code)")
                }
            }

            override fun onFailure(ws: WebSocket, t: Throwable, response: Response?) {
                if (webSocket == ws) {
                    webSocket = null
                    val msg = response?.let { "HTTP ${it.code}" } ?: (t.message ?: "网络异常")
                    scheduleReconnect("连接失败: $msg")
                }
            }
        })
    }

    private fun startAppPing(ws: WebSocket) {
        pingJob?.cancel()
        pingJob = scope.launch {
            while (isActive) {
                delay(25_000)
                if (!ws.send("""{"type":"ping"}""")) {
                    break
                }
            }
        }
    }

    private fun scheduleReconnect(reason: String) {
        pingJob?.cancel()
        AuditRepository.setConnectionState(ConnectionState.DISCONNECTED, reason)
        updateForegroundText("重连中 · $reason")
        reconnectJob?.cancel()
        reconnectJob = scope.launch {
            delay(backoffMs)
            backoffMs = (backoffMs * 2).coerceAtMost(25_000L)
            connectNow()
        }
    }

    private fun handleWsMessage(text: String) {
        val obj = runCatching { JSONObject(text) }.getOrNull() ?: return
        when (obj.optString("type")) {
            "init" -> {
                AuditRepository.onWsInit(obj.optJSONObject("status"), obj.optJSONArray("approvals"))
                val curSettings = AuditRepository.status.value.settings
                val requireApp = curSettings.highRiskRequireApp
                val autoApprove = curSettings.timeoutAction == "approve"
                AuditRepository.pendingApprovals.value.forEach {
                    AuditNotifier.showApproval(this, it, requireApp, autoApprove)
                }
                val macText = if (AuditRepository.status.value.macOnline) "Mac 在线" else "Mac 离线"
                updateForegroundText("在线 · $macText")
            }

            "mac.status" -> {
                AuditRepository.onMacStatus(obj.optJSONObject("status"))
                val macText = if (AuditRepository.status.value.macOnline) "Mac 在线" else "Mac 离线"
                updateForegroundText("在线 · $macText")
            }

            "settings.updated" -> {
                AuditRepository.onSettingsUpdated(obj.optJSONObject("settings"))
            }

            "approval.created" -> {
                val aObj = obj.optJSONObject("approval") ?: return
                val item = ApprovalItem.fromJson(aObj)
                AuditRepository.onApprovalCreated(item)
                val curSettings = AuditRepository.status.value.settings
                AuditNotifier.showApproval(
                    this,
                    item,
                    curSettings.highRiskRequireApp,
                    curSettings.timeoutAction == "approve"
                )
            }

            "approval.updated" -> {
                val aObj = obj.optJSONObject("approval") ?: return
                val item = ApprovalItem.fromJson(aObj)
                AuditRepository.onApprovalUpdated(item)
                AuditNotifier.cancelApproval(this, item.id)
                AuditRepository.messages.value
                    .filter { it.approvalId == item.id }
                    .forEach { AuditNotifier.cancelMessage(this, it.id) }
            }

            "message.created" -> {
                val mObj = obj.optJSONObject("message") ?: return
                val msg = AuditMessage.fromJson(mObj)
                AuditRepository.onMessageCreated(msg)
                AuditNotifier.showMessage(this, msg)
            }
        }
    }

    private fun updateForegroundText(@Suppress("UNUSED_PARAMETER") text: String) {
        // Intentionally no-op: do not re-notify the foreground service notification on heartbeat/reconnect,
        // so it never pops back up in the notification shade or lockscreen.
    }
}
