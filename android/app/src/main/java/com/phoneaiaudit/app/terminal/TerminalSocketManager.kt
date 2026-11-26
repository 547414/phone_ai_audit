package com.phoneaiaudit.app.terminal

import android.util.Base64
import com.phoneaiaudit.app.data.AuditRepository
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONObject

class TerminalSocketManager(
    private val onOutputBase64: (String) -> Unit,
    private val onSystemLine: (String) -> Unit,
    private val onRemoteSize: (cols: Int, rows: Int) -> Unit,
    private val onRemoteTarget: (String) -> Unit = {},
    private val onStateChange: (connected: Boolean, label: String) -> Unit
) {
    private var ws: WebSocket? = null
    @Volatile
    private var isOpen = false

    fun connect(
        ticket: String,
        mode: String,
        target: String,
        cols: Int,
        rows: Int,
        agyAccount: String = "",
        cwd: String = "",
        displayLabel: String = ""
    ) {
        disconnect()
        val cfg = AuditRepository.config
        val req = Request.Builder()
            .url("${cfg.wsBaseUrl()}/api/v1/ws/terminal")
            .header("Authorization", "Bearer ${cfg.appToken}")
            .header("X-Terminal-Ticket", ticket)
            .build()

        onStateChange(false, "正在连接终端...")
        ws = AuditRepository.httpClient.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                isOpen = true
                val openPayload = JSONObject().apply {
                    put("type", "open")
                    put("mode", mode)
                    put("target", target)
                    if (agyAccount.isNotBlank()) put("agy_account", agyAccount)
                    if (cwd.isNotBlank()) put("cwd", cwd)
                    put("cols", cols.coerceAtLeast(20))
                    put("rows", rows.coerceAtLeast(10))
                }
                webSocket.send(openPayload.toString())
                val initialLabel = when {
                    displayLabel.isNotBlank() -> displayLabel
                    target.isNotBlank() -> target
                    mode == "new_agy" && agyAccount.isNotBlank() -> "agy · $agyAccount"
                    mode == "new_agy" -> "Antigravity (agy)"
                    else -> "新建 Mac 终端"
                }
                onStateChange(true, initialLabel)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                val b64 = Base64.encodeToString(bytes.toByteArray(), Base64.NO_WRAP)
                onOutputBase64(b64)
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                val obj = runCatching { JSONObject(text) }.getOrNull() ?: return
                when (obj.optString("type")) {
                    "opened" -> {
                        val c = obj.optInt("cols", 0)
                        val r = obj.optInt("rows", 0)
                        if (c >= 20 && r >= 8) {
                            onRemoteSize(c, r)
                        }
                        val remoteTarget = obj.optString("target").trim()
                        if (remoteTarget.isNotBlank()) {
                            onRemoteTarget(remoteTarget)
                            onStateChange(true, remoteTarget)
                        }
                    }
                    "error" -> {
                        onSystemLine("[错误] ${obj.optString("error")}")
                        onStateChange(false, "连接出错")
                    }
                    "closed" -> {
                        onSystemLine("[终端已断开: ${obj.optString("reason")}]")
                        isOpen = false
                        onStateChange(false, "已断开")
                    }
                }
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (ws == webSocket) {
                    isOpen = false
                    ws = null
                    onStateChange(false, "已断开")
                }
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (ws == webSocket) {
                    isOpen = false
                    ws = null
                    val err = response?.let { "HTTP ${it.code}" } ?: (t.message ?: "连接失败")
                    onSystemLine("[连接失败: $err]")
                    onStateChange(false, "连接失败")
                }
            }
        })
    }

    fun sendInput(text: String) {
        if (!isOpen || text.isEmpty()) return
        val bytes = text.toByteArray(Charsets.UTF_8)
        ws?.send(bytes.toByteString(0, bytes.size))
    }

    fun resize(cols: Int, rows: Int) {
        if (!isOpen || cols <= 0 || rows <= 0) return
        val payload = JSONObject().apply {
            put("type", "resize")
            put("cols", cols)
            put("rows", rows)
        }
        ws?.send(payload.toString())
    }

    fun disconnect() {
        val cur = ws
        ws = null
        isOpen = false
        if (cur != null) {
            runCatching { cur.send("""{"type":"close"}""") }
            runCatching { cur.close(1000, "user_close") }
        }
        onStateChange(false, "未连接")
    }
}
