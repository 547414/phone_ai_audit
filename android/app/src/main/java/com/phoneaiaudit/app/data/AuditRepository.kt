package com.phoneaiaudit.app.data

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.TimeUnit

object AuditRepository {
    private val jsonMedia = "application/json; charset=utf-8".toMediaType()

    lateinit var config: ConfigStore
        private set

    val httpClient: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(12, TimeUnit.SECONDS)
        .readTimeout(25, TimeUnit.SECONDS)
        .writeTimeout(15, TimeUnit.SECONDS)
        .pingInterval(25, TimeUnit.SECONDS)
        .build()

    val longHttpClient: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(12, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .writeTimeout(30, TimeUnit.SECONDS)
        .build()

    private val _connectionState = MutableStateFlow(ConnectionState.DISCONNECTED)
    val connectionState: StateFlow<ConnectionState> = _connectionState.asStateFlow()

    private val _status = MutableStateFlow(StatusSnapshot())
    val status: StateFlow<StatusSnapshot> = _status.asStateFlow()

    private val _pendingApprovals = MutableStateFlow<List<ApprovalItem>>(emptyList())
    val pendingApprovals: StateFlow<List<ApprovalItem>> = _pendingApprovals.asStateFlow()

    private val _recentApprovals = MutableStateFlow<List<ApprovalItem>>(emptyList())
    val recentApprovals: StateFlow<List<ApprovalItem>> = _recentApprovals.asStateFlow()

    private val _messages = MutableStateFlow<List<AuditMessage>>(emptyList())
    val messages: StateFlow<List<AuditMessage>> = _messages.asStateFlow()

    private val _tmuxSessions = MutableStateFlow<List<TmuxSession>>(emptyList())
    val tmuxSessions: StateFlow<List<TmuxSession>> = _tmuxSessions.asStateFlow()

    private val _selectedTab = MutableStateFlow(0)
    val selectedTab: StateFlow<Int> = _selectedTab.asStateFlow()

    private val _pendingTerminalLaunch = MutableStateFlow<TerminalLaunchRequest?>(null)
    val pendingTerminalLaunch: StateFlow<TerminalLaunchRequest?> = _pendingTerminalLaunch.asStateFlow()

    private val _lastError = MutableStateFlow<String?>(null)
    val lastError: StateFlow<String?> = _lastError.asStateFlow()

    private val _configVersion = MutableStateFlow(0)
    val configVersion: StateFlow<Int> = _configVersion.asStateFlow()

    private val _themeMode = MutableStateFlow("system")
    val themeMode: StateFlow<String> = _themeMode.asStateFlow()

    private val _isTerminalActive = MutableStateFlow(false)
    val isTerminalActive: StateFlow<Boolean> = _isTerminalActive.asStateFlow()

    private val _isFileOverlayActive = MutableStateFlow(false)
    val isFileOverlayActive: StateFlow<Boolean> = _isFileOverlayActive.asStateFlow()

    private val _pendingFileOpen = MutableStateFlow<FileOpenRequest?>(null)
    val pendingFileOpen: StateFlow<FileOpenRequest?> = _pendingFileOpen.asStateFlow()

    private val _usageSummary = MutableStateFlow<UsageSummary?>(null)
    val usageSummary: StateFlow<UsageSummary?> = _usageSummary.asStateFlow()

    private val _agyProfiles = MutableStateFlow<AgyProfilesResponse?>(null)
    val agyProfiles: StateFlow<AgyProfilesResponse?> = _agyProfiles.asStateFlow()

    fun setTerminalActive(active: Boolean) {
        _isTerminalActive.value = active
    }

    fun setFileOverlayActive(active: Boolean) {
        _isFileOverlayActive.value = active
    }

    fun requestOpenFile(path: String, jumpLine: Int = 0, kindHint: String = "") {
        _pendingFileOpen.value = FileOpenRequest(path, jumpLine, kindHint)
        _selectedTab.value = 2
    }

    fun consumePendingFileOpen(): FileOpenRequest? {
        val v = _pendingFileOpen.value
        _pendingFileOpen.value = null
        return v
    }

    fun notifyConfigChanged() {
        _configVersion.value = _configVersion.value + 1
    }

    fun setThemeMode(mode: String) {
        if (::config.isInitialized) {
            config.themeMode = mode
        }
        _themeMode.value = mode
    }

    fun init(context: Context) {
        if (!::config.isInitialized) {
            config = ConfigStore(context)
            _themeMode.value = config.themeMode
        }
    }

    fun setConnectionState(state: ConnectionState, error: String? = null) {
        _connectionState.value = state
        _lastError.value = error
    }

    fun selectTab(index: Int) {
        _selectedTab.value = index
    }

    fun requestOpenTerminal(mode: String, target: String = "") {
        _pendingTerminalLaunch.value = TerminalLaunchRequest(mode, target)
        _selectedTab.value = 1
    }

    fun consumeTerminalLaunch(): TerminalLaunchRequest? {
        val v = _pendingTerminalLaunch.value
        _pendingTerminalLaunch.value = null
        return v
    }

    fun onWsInit(statusObj: JSONObject?, pendingArr: JSONArray?) {
        val snap = StatusSnapshot.fromJson(statusObj)
        _status.value = snap
        _tmuxSessions.value = snap.tmuxSessions
        if (pendingArr != null) {
            val list = mutableListOf<ApprovalItem>()
            for (i in 0 until pendingArr.length()) {
                pendingArr.optJSONObject(i)?.let { list.add(ApprovalItem.fromJson(it)) }
            }
            _pendingApprovals.value = list.sortedByDescending { it.createdAt }
        }
    }

    fun onMacStatus(statusObj: JSONObject?) {
        val snap = StatusSnapshot.fromJson(statusObj)
        _status.value = snap
        _tmuxSessions.value = snap.tmuxSessions
    }

    fun onSettingsUpdated(settingsObj: JSONObject?) {
        val s = RelaySettings.fromJson(settingsObj)
        _status.value = _status.value.copy(settings = s)
    }

    fun onApprovalCreated(item: ApprovalItem) {
        val cur = _pendingApprovals.value.filterNot { it.id == item.id }
        _pendingApprovals.value = listOf(item) + cur
    }

    fun onApprovalExpiryUpdated(item: ApprovalItem) {
        val cur = _pendingApprovals.value
        if (cur.any { it.id == item.id }) {
            _pendingApprovals.value = cur.map { if (it.id == item.id) item else it }
        }
    }

    fun onApprovalUpdated(item: ApprovalItem) {
        val nextPending = _pendingApprovals.value.filterNot { it.id == item.id }
        _pendingApprovals.value = nextPending
        val hist = _recentApprovals.value.filterNot { it.id == item.id }
        _recentApprovals.value = (listOf(item) + hist).take(40)
        val now = System.currentTimeMillis()
        val nextMessages = _messages.value.map { m ->
            if (m.approvalId == item.id && m.readAt == 0L) m.copy(readAt = now) else m
        }
        _messages.value = nextMessages
        _status.value = _status.value.copy(
            pendingCount = nextPending.size,
            unreadCount = nextMessages.count { it.readAt == 0L }
        )
    }

    fun onMessageCreated(msg: AuditMessage) {
        val cur = _messages.value.filterNot { it.id == msg.id }
        _messages.value = (listOf(msg) + cur).take(120)
    }

    private fun authedRequest(path: String): Request.Builder {
        return Request.Builder()
            .url("${config.serverUrl}$path")
            .header("Authorization", "Bearer ${config.appToken}")
    }

    suspend fun refreshAll(): Result<Unit> = withContext(Dispatchers.IO) {
        if (!config.isConfigured()) return@withContext Result.failure(IllegalStateException("未配置服务器"))
        runCatching {
            fetchStatus()
            fetchApprovals()
            fetchMessages()
            Unit
        }.onFailure {
            _lastError.value = it.message
        }
    }

    suspend fun fetchStatus(): Result<StatusSnapshot> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/status").get().build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                val snap = StatusSnapshot.fromJson(JSONObject(body))
                _status.value = snap
                if (snap.tmuxSessions.isNotEmpty() || !snap.macOnline) {
                    _tmuxSessions.value = snap.tmuxSessions
                }
                snap
            }
        }
    }

    suspend fun fetchApprovals(): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/approvals?limit=60").get().build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                val items = JSONObject(body).optJSONArray("items") ?: JSONArray()
                val pending = mutableListOf<ApprovalItem>()
                val recent = mutableListOf<ApprovalItem>()
                for (i in 0 until items.length()) {
                    val a = ApprovalItem.fromJson(items.getJSONObject(i))
                    if (a.isPending) pending.add(a) else recent.add(a)
                }
                _pendingApprovals.value = pending.sortedByDescending { it.createdAt }
                _recentApprovals.value = recent.sortedByDescending { it.createdAt }.take(40)
            }
        }
    }

    suspend fun fetchMessages(): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/messages?limit=80").get().build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                val items = JSONObject(body).optJSONArray("items") ?: JSONArray()
                val out = mutableListOf<AuditMessage>()
                for (i in 0 until items.length()) {
                    out.add(AuditMessage.fromJson(items.getJSONObject(i)))
                }
                _messages.value = out
            }
        }
    }

    suspend fun decideApproval(id: String, decision: String, reason: String = ""): Result<ApprovalItem> =
        withContext(Dispatchers.IO) {
            runCatching {
                val payload = JSONObject().apply {
                    put("decision", decision)
                    if (reason.isNotBlank()) put("reason", reason.trim())
                    put("scope", "once")
                }
                val req = authedRequest("/api/v1/approvals/$id/decide")
                    .post(payload.toString().toRequestBody(jsonMedia))
                    .build()
                httpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (resp.code == 409) {
                        val obj = JSONObject(body).optJSONObject("approval")
                        if (obj != null) {
                            val resolved = ApprovalItem.fromJson(obj)
                            onApprovalUpdated(resolved)
                            return@use resolved
                        }
                    }
                    if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                    val resolved = ApprovalItem.fromJson(JSONObject(body))
                    onApprovalUpdated(resolved)
                    resolved
                }
            }
        }

    suspend fun setApprovalHold(id: String, paused: Boolean): Result<ApprovalItem> =
        withContext(Dispatchers.IO) {
            runCatching {
                val payload = JSONObject().put("paused", paused)
                val req = authedRequest("/api/v1/approvals/$id/hold")
                    .post(payload.toString().toRequestBody(jsonMedia))
                    .build()
                httpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                    val updated = ApprovalItem.fromJson(JSONObject(body))
                    onApprovalExpiryUpdated(updated)
                    updated
                }
            }
        }

    suspend fun updateSettings(patch: JSONObject): Result<RelaySettings> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/settings")
                .put(patch.toString().toRequestBody(jsonMedia))
                .build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                val updated = RelaySettings.fromJson(JSONObject(body))
                _status.value = _status.value.copy(settings = updated)
                updated
            }
        }
    }

    suspend fun markAllMessagesRead(): Result<Unit> = markMessagesRead(emptyList())

    suspend fun markMessagesRead(ids: List<String>): Result<Unit> = withContext(Dispatchers.IO) {
        val now = System.currentTimeMillis()
        val idSet = ids.toSet()
        // Optimistic UI update so badge & unread indicator clear instantly
        _messages.value = _messages.value.map { m ->
            if (m.readAt == 0L && (idSet.isEmpty() || m.id in idSet)) m.copy(readAt = now) else m
        }
        runCatching {
            val payload = if (ids.isEmpty()) {
                "{}"
            } else {
                val arr = JSONArray()
                ids.forEach { arr.put(it) }
                JSONObject().put("ids", arr).toString()
            }
            val req = authedRequest("/api/v1/messages/read")
                .post(payload.toRequestBody(jsonMedia))
                .build()
            httpClient.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) error("HTTP ${resp.code}")
            }
        }
    }

    suspend fun refreshTmuxSessions(): Result<List<TmuxSession>> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/terminals/sessions").get().build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                val obj = JSONObject(body)
                val online = obj.optBoolean("mac_online", false)
                val sessions = TmuxSession.listFromJson(obj.optJSONArray("sessions"))
                _status.value = _status.value.copy(macOnline = online, tmuxSessions = sessions)
                _tmuxSessions.value = sessions
                sessions
            }
        }
    }

    suspend fun issueTerminalTicket(): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/terminals/ticket")
                .post("{}".toRequestBody(jsonMedia))
                .build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) {
                    val errMsg = runCatching { JSONObject(body).optString("error") }.getOrDefault(body)
                    error(errMsg.ifBlank { "HTTP ${resp.code}" })
                }
                JSONObject(body).getString("ticket")
            }
        }
    }

    suspend fun setTerminalNote(sessionName: String, note: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val encoded = java.net.URLEncoder.encode(sessionName, "UTF-8").replace("+", "%20")
            val payload = JSONObject().put("note", note.trim()).toString()
            val req = authedRequest("/api/v1/terminals/sessions/$encoded/note")
                .put(payload.toRequestBody(jsonMedia))
                .build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                _tmuxSessions.value = _tmuxSessions.value.map {
                    if (it.name == sessionName) it.copy(note = note.trim()) else it
                }
            }
        }
    }

    suspend fun killTmuxSession(sessionName: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val encoded = java.net.URLEncoder.encode(sessionName, "UTF-8").replace("+", "%20")
            val req = authedRequest("/api/v1/terminals/sessions/$encoded")
                .delete()
                .build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error("HTTP ${resp.code}: $body")
                _tmuxSessions.value = _tmuxSessions.value.filterNot { it.name == sessionName }
            }
        }
    }

    // --- Remote Mac File Browser / Preview / Edit & Git Workbench ---

    private fun enc(v: String): String = java.net.URLEncoder.encode(v, "UTF-8")

    private fun parseErrorMsg(code: Int, body: String): String {
        val msg = runCatching { JSONObject(body).optString("error") }.getOrDefault("")
        return msg.ifBlank { "HTTP $code" }
    }

    fun buildStreamUrl(
        path: String,
        kind: String,
        page: Int = 0,
        dpi: Int = 0,
        maxDim: Int = 0
    ): String {
        val sb = StringBuilder("${config.serverUrl}/api/v1/fs/stream?path=${enc(path)}&kind=${enc(kind)}")
        if (page > 0) sb.append("&page=$page")
        if (dpi > 0) sb.append("&dpi=$dpi")
        if (maxDim > 0) sb.append("&max_dim=$maxDim")
        return sb.toString()
    }

    suspend fun fetchStreamBytes(url: String): Result<ByteArray> = withContext(Dispatchers.IO) {
        runCatching {
            val req = Request.Builder()
                .url(url)
                .header("Authorization", "Bearer ${config.appToken}")
                .header("Cache-Control", "no-store")
                .get()
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) {
                    val body = resp.body?.string().orEmpty()
                    error(parseErrorMsg(resp.code, body))
                }
                resp.body?.bytes() ?: ByteArray(0)
            }
        }
    }

    suspend fun fetchStreamText(url: String): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val req = Request.Builder()
                .url(url)
                .header("Authorization", "Bearer ${config.appToken}")
                .header("Cache-Control", "no-store")
                .get()
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                body
            }
        }
    }

    suspend fun fsList(path: String = "", hidden: Boolean = false, sort: String = "name"): Result<FsListResult> =
        withContext(Dispatchers.IO) {
            runCatching {
                val url = "/api/v1/fs/list?path=${enc(path)}&hidden=${if (hidden) "1" else "0"}&sort=${enc(sort)}"
                val req = authedRequest(url).get().build()
                longHttpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                    FsListResult.fromJson(JSONObject(body))
                }
            }
        }

    suspend fun fsSearch(path: String, query: String, mode: String): Result<List<FsSearchHit>> =
        withContext(Dispatchers.IO) {
            runCatching {
                val url = "/api/v1/fs/search?path=${enc(path)}&q=${enc(query)}&mode=${enc(mode)}"
                val req = authedRequest(url).get().build()
                longHttpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                    val arr = JSONObject(body).optJSONArray("items") ?: JSONArray()
                    val out = mutableListOf<FsSearchHit>()
                    for (i in 0 until arr.length()) {
                        arr.optJSONObject(i)?.let { out.add(FsSearchHit.fromJson(it)) }
                    }
                    out
                }
            }
        }

    suspend fun fsRead(
        path: String,
        offsetLine: Int = 1,
        limitLines: Int = 400,
        tail: Boolean = false
    ): Result<FsReadResult> = withContext(Dispatchers.IO) {
        runCatching {
            val url = "/api/v1/fs/read?path=${enc(path)}&offset_line=$offsetLine&limit_lines=$limitLines&tail=${if (tail) "1" else "0"}"
            val req = authedRequest(url).get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                FsReadResult.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun fsWrite(path: String, content: String, expectedSha256: String): Result<FsWriteResult> =
        withContext(Dispatchers.IO) {
            runCatching {
                val payload = JSONObject().apply {
                    put("path", path)
                    put("content", content)
                    put("expected_sha256", expectedSha256)
                }
                val req = authedRequest("/api/v1/fs/write")
                    .put(payload.toString().toRequestBody(jsonMedia))
                    .build()
                longHttpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (resp.code == 409) {
                        val obj = runCatching { JSONObject(body) }.getOrNull()
                        val data = obj?.optJSONObject("data")
                        throw FsConflictException(
                            message = obj?.optString("error").orEmpty().ifBlank { "文件已被外部修改" },
                            currentSha256 = data?.optString("current_sha256").orEmpty(),
                            currentText = data?.optString("current_text").orEmpty()
                        )
                    }
                    if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                    FsWriteResult.fromJson(JSONObject(body))
                }
            }
        }

    suspend fun fsMkdir(path: String): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path)
            val req = authedRequest("/api/v1/fs/mkdir").post(payload.toString().toRequestBody(jsonMedia)).build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                JSONObject(body).optString("path", path)
            }
        }
    }

    suspend fun fsTouch(path: String): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path)
            val req = authedRequest("/api/v1/fs/touch").post(payload.toString().toRequestBody(jsonMedia)).build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                JSONObject(body).optString("path", path)
            }
        }
    }

    suspend fun fsRename(path: String, newName: String): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path).put("new_name", newName)
            val req = authedRequest("/api/v1/fs/rename").post(payload.toString().toRequestBody(jsonMedia)).build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                JSONObject(body).optString("path", path)
            }
        }
    }

    suspend fun fsTrash(path: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path)
            val req = authedRequest("/api/v1/fs/trash").post(payload.toString().toRequestBody(jsonMedia)).build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
            }
        }
    }

    suspend fun fsPdfMeta(path: String): Result<PdfMeta> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/fs/pdf/meta?path=${enc(path)}").get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                PdfMeta.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun fsExcelMeta(path: String): Result<ExcelMeta> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/fs/excel/meta?path=${enc(path)}").get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                ExcelMeta.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun fsExcelSheet(
        path: String,
        sheet: String,
        rowStart: Int = 1,
        limit: Int = 150
    ): Result<ExcelSheetData> = withContext(Dispatchers.IO) {
        runCatching {
            val url = "/api/v1/fs/excel/sheet?path=${enc(path)}&sheet=${enc(sheet)}&row_start=$rowStart&limit=$limit"
            val req = authedRequest(url).get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                ExcelSheetData.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun fsExcelWriteCell(
        path: String,
        sheet: String,
        row: Int,
        col: Int,
        value: String,
        expectedSha256: String
    ): Result<FsWriteResult> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().apply {
                put("path", path)
                put("sheet", sheet)
                put("row", row)
                put("col", col)
                put("value", value)
                put("expected_sha256", expectedSha256)
            }
            val req = authedRequest("/api/v1/fs/excel/cell")
                .put(payload.toString().toRequestBody(jsonMedia))
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (resp.code == 409) {
                    val obj = runCatching { JSONObject(body) }.getOrNull()
                    val data = obj?.optJSONObject("data")
                    throw FsConflictException(
                        message = obj?.optString("error").orEmpty().ifBlank { "表格已被外部修改" },
                        currentSha256 = data?.optString("current_sha256").orEmpty(),
                        currentText = ""
                    )
                }
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                FsWriteResult.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun fsPptxOutline(path: String): Result<PptxOutline> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/fs/pptx/outline?path=${enc(path)}").get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                PptxOutline.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun gitStatus(path: String): Result<GitStatusResult> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/git/status?path=${enc(path)}").get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                GitStatusResult.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun gitDiff(
        path: String,
        file: String = "",
        staged: Boolean = false,
        commit: String = ""
    ): Result<GitDiffResult> = withContext(Dispatchers.IO) {
        runCatching {
            val url = "/api/v1/git/diff?path=${enc(path)}&file=${enc(file)}&staged=${if (staged) "1" else "0"}&commit=${enc(commit)}"
            val req = authedRequest(url).get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                GitDiffResult.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun gitCommit(path: String, files: List<String>, message: String): Result<GitCommitResult> =
        withContext(Dispatchers.IO) {
            runCatching {
                val arr = JSONArray()
                files.forEach { arr.put(it) }
                val payload = JSONObject().apply {
                    put("path", path)
                    put("files", arr)
                    put("message", message)
                }
                val req = authedRequest("/api/v1/git/commit")
                    .post(payload.toString().toRequestBody(jsonMedia))
                    .build()
                longHttpClient.newCall(req).execute().use { resp ->
                    val body = resp.body?.string().orEmpty()
                    if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                    GitCommitResult.fromJson(JSONObject(body))
                }
            }
        }

    suspend fun gitPush(path: String): Result<GitPushResult> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path)
            val req = authedRequest("/api/v1/git/push")
                .post(payload.toString().toRequestBody(jsonMedia))
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                GitPushResult.fromJson(JSONObject(body))
            }
        }
    }

    suspend fun gitPull(path: String): Result<String> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path)
            val req = authedRequest("/api/v1/git/pull")
                .post(payload.toString().toRequestBody(jsonMedia))
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                JSONObject(body).optString("output", "拉取完成")
            }
        }
    }

    suspend fun gitDiscard(path: String, file: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val payload = JSONObject().put("path", path).put("file", file)
            val req = authedRequest("/api/v1/git/discard")
                .post(payload.toString().toRequestBody(jsonMedia))
                .build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
            }
        }
    }

    suspend fun gitLog(path: String, limit: Int = 20): Result<List<GitLogEntry>> = withContext(Dispatchers.IO) {
        runCatching {
            val req = authedRequest("/api/v1/git/log?path=${enc(path)}&limit=$limit").get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                val arr = JSONObject(body).optJSONArray("items") ?: JSONArray()
                val out = mutableListOf<GitLogEntry>()
                for (i in 0 until arr.length()) {
                    arr.optJSONObject(i)?.let { out.add(GitLogEntry.fromJson(it)) }
                }
                out
            }
        }
    }

    suspend fun fetchUsage(force: Boolean = false): Result<UsageSummary> = withContext(Dispatchers.IO) {
        if (!config.isConfigured()) return@withContext Result.failure(IllegalStateException("未配置服务器"))
        runCatching {
            val url = if (force) "/api/v1/usage?force=1" else "/api/v1/usage"
            val req = authedRequest(url).get().build()
            longHttpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                val summary = UsageSummary.fromJson(JSONObject(body))
                _usageSummary.value = summary
                summary
            }
        }
    }

    suspend fun fetchAgyProfiles(): Result<AgyProfilesResponse> = withContext(Dispatchers.IO) {
        if (!config.isConfigured()) return@withContext Result.failure(IllegalStateException("未配置服务器"))
        runCatching {
            val req = authedRequest("/api/v1/terminals/agy-profiles").get().build()
            httpClient.newCall(req).execute().use { resp ->
                val body = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseErrorMsg(resp.code, body))
                val parsed = AgyProfilesResponse.fromJson(JSONObject(body))
                _agyProfiles.value = parsed
                parsed
            }
        }
    }
}
