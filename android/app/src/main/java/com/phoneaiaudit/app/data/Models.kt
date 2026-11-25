package com.phoneaiaudit.app.data

import org.json.JSONArray
import org.json.JSONObject

enum class ConnectionState {
    DISCONNECTED,
    CONNECTING,
    CONNECTED
}

data class ApprovalItem(
    val id: String,
    val agent: String,
    val sessionId: String,
    val tmuxSession: String,
    val taskTitle: String,
    val projectName: String,
    val cwd: String,
    val toolName: String,
    val summary: String,
    val payloadJson: String,
    val riskLevel: String,
    val riskReasons: List<String>,
    val status: String,
    val decisionReason: String,
    val decisionScope: String,
    val createdAt: Long,
    val expiresAt: Long,
    val decidedAt: Long
) {
    val isHighRisk: Boolean get() = riskLevel.equals("high", ignoreCase = true)
    val isPending: Boolean get() = status == "pending"

    fun extractTargetFilePath(): String? {
        if (payloadJson.isBlank()) return null
        return runCatching {
            val obj = JSONObject(payloadJson)
            val keys = listOf("file_path", "TargetFile", "path", "AbsolutePath", "file")
            for (k in keys) {
                val v = obj.optString(k).trim()
                if (v.startsWith("/")) return@runCatching v
            }
            null
        }.getOrNull()
    }

    companion object {
        fun fromJson(o: JSONObject): ApprovalItem {
            val reasons = mutableListOf<String>()
            val arr = o.optJSONArray("risk_reasons")
            if (arr != null) {
                for (i in 0 until arr.length()) {
                    val v = arr.optString(i)
                    if (v.isNotBlank()) reasons.add(v)
                }
            }
            return ApprovalItem(
                id = o.optString("id"),
                agent = o.optString("agent"),
                sessionId = o.optString("session_id"),
                tmuxSession = o.optString("tmux_session"),
                taskTitle = o.optString("task_title"),
                projectName = o.optString("project_name"),
                cwd = o.optString("cwd"),
                toolName = o.optString("tool_name"),
                summary = o.optString("summary"),
                payloadJson = o.optString("payload_json"),
                riskLevel = o.optString("risk_level", "low"),
                riskReasons = reasons,
                status = o.optString("status", "pending"),
                decisionReason = o.optString("decision_reason"),
                decisionScope = o.optString("decision_scope"),
                createdAt = o.optLong("created_at"),
                expiresAt = o.optLong("expires_at"),
                decidedAt = o.optLong("decided_at")
            )
        }
    }
}

data class AuditMessage(
    val id: String,
    val agent: String,
    val eventType: String,
    val priority: String,
    val title: String,
    val body: String,
    val taskTitle: String,
    val projectName: String,
    val tmuxSession: String,
    val approvalId: String,
    val createdAt: Long,
    val readAt: Long
) {
    companion object {
        fun fromJson(o: JSONObject): AuditMessage = AuditMessage(
            id = o.optString("id"),
            agent = o.optString("agent"),
            eventType = o.optString("event_type"),
            priority = o.optString("priority", "default"),
            title = o.optString("title"),
            body = o.optString("body"),
            taskTitle = o.optString("task_title"),
            projectName = o.optString("project_name"),
            tmuxSession = o.optString("tmux_session"),
            approvalId = o.optString("approval_id"),
            createdAt = o.optLong("created_at"),
            readAt = o.optLong("read_at")
        )
    }
}

data class RelaySettings(
    val approvalMode: String = "auto",
    val idleThresholdSec: Int = 60,
    val defaultTimeoutSec: Int = 180,
    val timeoutAction: String = "fallback",
    val terminalEnabled: Boolean = true,
    val filesEnabled: Boolean = true,
    val macAwakeEnabled: Boolean = false,
    val highRiskRequireApp: Boolean = false
) {
    companion object {
        fun fromJson(o: JSONObject?): RelaySettings {
            if (o == null) return RelaySettings()
            return RelaySettings(
                approvalMode = o.optString("approval_mode", "auto"),
                idleThresholdSec = o.optInt("idle_threshold_sec", 60),
                defaultTimeoutSec = o.optInt("default_timeout_sec", 180),
                timeoutAction = o.optString("timeout_action", "fallback"),
                terminalEnabled = o.optBoolean("terminal_enabled", true),
                filesEnabled = o.optBoolean("files_enabled", true),
                macAwakeEnabled = o.optBoolean("mac_awake_enabled", false),
                highRiskRequireApp = o.optBoolean("high_risk_require_app", false)
            )
        }
    }
}

data class TmuxSession(
    val name: String,
    val note: String = "",
    val windows: Int,
    val attached: Boolean,
    val command: String,
    val agent: String,
    val cwd: String,
    val projectName: String,
    val activityAt: Long
) {
    companion object {
        fun fromJson(o: JSONObject): TmuxSession = TmuxSession(
            name = o.optString("name"),
            note = o.optString("note"),
            windows = o.optInt("windows", 1),
            attached = o.optBoolean("attached", false),
            command = o.optString("command"),
            agent = o.optString("agent"),
            cwd = o.optString("cwd"),
            projectName = o.optString("project_name"),
            activityAt = o.optLong("activity_at")
        )

        fun listFromJson(arr: JSONArray?): List<TmuxSession> {
            if (arr == null) return emptyList()
            val out = mutableListOf<TmuxSession>()
            for (i in 0 until arr.length()) {
                arr.optJSONObject(i)?.let { out.add(fromJson(it)) }
            }
            return out
        }
    }
}

data class StatusSnapshot(
    val macOnline: Boolean = false,
    val macHostname: String = "",
    val macLastSeenAt: Long = 0L,
    val appOnlineCount: Int = 0,
    val pendingCount: Int = 0,
    val unreadCount: Int = 0,
    val settings: RelaySettings = RelaySettings(),
    val tmuxSessions: List<TmuxSession> = emptyList()
) {
    companion object {
        fun fromJson(o: JSONObject?): StatusSnapshot {
            if (o == null) return StatusSnapshot()
            return StatusSnapshot(
                macOnline = o.optBoolean("mac_online", false),
                macHostname = o.optString("mac_hostname"),
                macLastSeenAt = o.optLong("mac_last_seen_at"),
                appOnlineCount = o.optInt("app_online_count"),
                pendingCount = o.optInt("pending_count"),
                unreadCount = o.optInt("unread_count"),
                settings = RelaySettings.fromJson(o.optJSONObject("settings")),
                tmuxSessions = TmuxSession.listFromJson(o.optJSONArray("tmux_sessions"))
            )
        }
    }
}

data class TerminalLaunchRequest(
    val mode: String, // "tmux_attach" or "new_shell"
    val target: String = ""
)

data class FileOpenRequest(
    val path: String,
    val jumpLine: Int = 0,
    val kindHint: String = ""
)

// --- Remote Mac File & Git Models ---

data class GitRepoInfo(
    val isRepo: Boolean = false,
    val root: String = "",
    val repoName: String = "",
    val branch: String = "",
    val remote: String = "",
    val upstream: String = "",
    val ahead: Int = 0,
    val behind: Int = 0,
    val dirtyCount: Int = 0,
    val protected: Boolean = false
) {
    companion object {
        fun fromJson(o: JSONObject?): GitRepoInfo {
            if (o == null) return GitRepoInfo()
            return GitRepoInfo(
                isRepo = o.optBoolean("is_repo", false),
                root = o.optString("root"),
                repoName = o.optString("repo_name"),
                branch = o.optString("branch"),
                remote = o.optString("remote"),
                upstream = o.optString("upstream"),
                ahead = o.optInt("ahead", 0),
                behind = o.optInt("behind", 0),
                dirtyCount = o.optInt("dirty_count", 0),
                protected = o.optBoolean("protected", false)
            )
        }
    }
}

data class FsEntry(
    val name: String,
    val path: String,
    val isDir: Boolean,
    val size: Long,
    val modTime: Long,
    val ext: String,
    val kind: String,
    val gitStatus: String,
    val isSensitive: Boolean
) {
    companion object {
        fun fromJson(o: JSONObject): FsEntry = FsEntry(
            name = o.optString("name"),
            path = o.optString("path"),
            isDir = o.optBoolean("is_dir", false),
            size = o.optLong("size", 0L),
            modTime = o.optLong("mod_time", 0L),
            ext = o.optString("ext"),
            kind = o.optString("kind", "binary"),
            gitStatus = o.optString("git_status"),
            isSensitive = o.optBoolean("is_sensitive", false)
        )
    }
}

data class FsListResult(
    val path: String = "",
    val parentPath: String = "",
    val roots: List<String> = emptyList(),
    val gitRepo: GitRepoInfo = GitRepoInfo(),
    val entries: List<FsEntry> = emptyList()
) {
    companion object {
        fun fromJson(o: JSONObject): FsListResult {
            val roots = mutableListOf<String>()
            val rArr = o.optJSONArray("roots")
            if (rArr != null) {
                for (i in 0 until rArr.length()) {
                    rArr.optString(i).takeIf { it.isNotBlank() }?.let { roots.add(it) }
                }
            }
            val entries = mutableListOf<FsEntry>()
            val eArr = o.optJSONArray("entries")
            if (eArr != null) {
                for (i in 0 until eArr.length()) {
                    eArr.optJSONObject(i)?.let { entries.add(FsEntry.fromJson(it)) }
                }
            }
            return FsListResult(
                path = o.optString("path"),
                parentPath = o.optString("parent_path"),
                roots = roots,
                gitRepo = GitRepoInfo.fromJson(o.optJSONObject("git_repo")),
                entries = entries
            )
        }
    }
}

data class FsSearchHit(
    val name: String,
    val path: String,
    val relPath: String,
    val isDir: Boolean,
    val kind: String,
    val line: Int,
    val text: String
) {
    companion object {
        fun fromJson(o: JSONObject): FsSearchHit = FsSearchHit(
            name = o.optString("name"),
            path = o.optString("path"),
            relPath = o.optString("rel_path"),
            isDir = o.optBoolean("is_dir", false),
            kind = o.optString("kind", "code"),
            line = o.optInt("line", 0),
            text = o.optString("text")
        )
    }
}

data class FsReadResult(
    val path: String,
    val name: String,
    val kind: String,
    val ext: String,
    val size: Long,
    val modTime: Long,
    val sha256: String,
    val content: String,
    val totalLines: Int,
    val offsetLine: Int,
    val returnedLines: Int,
    val editable: Boolean,
    val truncated: Boolean,
    val hasMore: Boolean,
    val isSensitive: Boolean
) {
    companion object {
        fun fromJson(o: JSONObject): FsReadResult = FsReadResult(
            path = o.optString("path"),
            name = o.optString("name"),
            kind = o.optString("kind", "code"),
            ext = o.optString("ext"),
            size = o.optLong("size", 0L),
            modTime = o.optLong("mod_time", 0L),
            sha256 = o.optString("sha256"),
            content = o.optString("content"),
            totalLines = o.optInt("total_lines", 0),
            offsetLine = o.optInt("offset_line", 1),
            returnedLines = o.optInt("returned_lines", 0),
            editable = o.optBoolean("editable", false),
            truncated = o.optBoolean("truncated", false),
            hasMore = o.optBoolean("has_more", false),
            isSensitive = o.optBoolean("is_sensitive", false)
        )
    }
}

data class FsWriteResult(
    val path: String,
    val sha256: String,
    val size: Long,
    val modTime: Long
) {
    companion object {
        fun fromJson(o: JSONObject): FsWriteResult = FsWriteResult(
            path = o.optString("path"),
            sha256 = o.optString("sha256"),
            size = o.optLong("size", 0L),
            modTime = o.optLong("mod_time", 0L)
        )
    }
}

class FsConflictException(
    message: String,
    val currentSha256: String,
    val currentText: String
) : Exception(message)

data class PdfMeta(
    val path: String,
    val name: String,
    val pages: Int,
    val size: Long
) {
    companion object {
        fun fromJson(o: JSONObject): PdfMeta = PdfMeta(
            path = o.optString("path"),
            name = o.optString("name"),
            pages = o.optInt("pages", 1).coerceAtLeast(1),
            size = o.optLong("size", 0L)
        )
    }
}

data class ExcelMeta(
    val path: String,
    val name: String,
    val sheets: List<String>,
    val sha256: String,
    val isCsv: Boolean
) {
    companion object {
        fun fromJson(o: JSONObject): ExcelMeta {
            val sheets = mutableListOf<String>()
            val arr = o.optJSONArray("sheets")
            if (arr != null) {
                for (i in 0 until arr.length()) {
                    arr.optString(i).takeIf { it.isNotBlank() }?.let { sheets.add(it) }
                }
            }
            return ExcelMeta(
                path = o.optString("path"),
                name = o.optString("name"),
                sheets = sheets,
                sha256 = o.optString("sha256"),
                isCsv = o.optBoolean("is_csv", false)
            )
        }
    }
}

data class ExcelSheetData(
    val path: String,
    val sheet: String,
    val rowStart: Int,
    val totalRows: Int,
    val totalCols: Int,
    val hasMore: Boolean,
    val sha256: String,
    val rows: List<List<String>>
) {
    companion object {
        fun fromJson(o: JSONObject): ExcelSheetData {
            val rows = mutableListOf<List<String>>()
            val rArr = o.optJSONArray("rows")
            if (rArr != null) {
                for (i in 0 until rArr.length()) {
                    val cArr = rArr.optJSONArray(i) ?: continue
                    val row = mutableListOf<String>()
                    for (j in 0 until cArr.length()) {
                        row.add(cArr.optString(j, ""))
                    }
                    rows.add(row)
                }
            }
            return ExcelSheetData(
                path = o.optString("path"),
                sheet = o.optString("sheet"),
                rowStart = o.optInt("row_start", 1),
                totalRows = o.optInt("total_rows", rows.size),
                totalCols = o.optInt("total_cols", 0),
                hasMore = o.optBoolean("has_more", false),
                sha256 = o.optString("sha256"),
                rows = rows
            )
        }
    }
}

data class SlideItem(
    val index: Int,
    val title: String,
    val lines: List<String>
)

data class PptxOutline(
    val path: String,
    val name: String,
    val slides: List<SlideItem>
) {
    companion object {
        fun fromJson(o: JSONObject): PptxOutline {
            val slides = mutableListOf<SlideItem>()
            val arr = o.optJSONArray("slides")
            if (arr != null) {
                for (i in 0 until arr.length()) {
                    val s = arr.optJSONObject(i) ?: continue
                    val lines = mutableListOf<String>()
                    val lArr = s.optJSONArray("lines")
                    if (lArr != null) {
                        for (j in 0 until lArr.length()) {
                            lines.add(lArr.optString(j, ""))
                        }
                    }
                    slides.add(
                        SlideItem(
                            index = s.optInt("index", i + 1),
                            title = s.optString("title"),
                            lines = lines
                        )
                    )
                }
            }
            return PptxOutline(
                path = o.optString("path"),
                name = o.optString("name"),
                slides = slides
            )
        }
    }
}

data class GitChangedFile(
    val path: String,
    val origPath: String,
    val indexStatus: String,
    val worktreeStatus: String,
    val statusCode: String,
    val staged: Boolean,
    val untracked: Boolean,
    val added: Int,
    val deleted: Int
) {
    companion object {
        fun fromJson(o: JSONObject): GitChangedFile = GitChangedFile(
            path = o.optString("path"),
            origPath = o.optString("orig_path"),
            indexStatus = o.optString("index_status"),
            worktreeStatus = o.optString("worktree_status"),
            statusCode = o.optString("status_code", "M"),
            staged = o.optBoolean("staged", false),
            untracked = o.optBoolean("untracked", false),
            added = o.optInt("added", 0),
            deleted = o.optInt("deleted", 0)
        )
    }
}

data class GitStatusResult(
    val repo: GitRepoInfo = GitRepoInfo(),
    val files: List<GitChangedFile> = emptyList()
) {
    companion object {
        fun fromJson(o: JSONObject): GitStatusResult {
            val list = mutableListOf<GitChangedFile>()
            val arr = o.optJSONArray("files")
            if (arr != null) {
                for (i in 0 until arr.length()) {
                    arr.optJSONObject(i)?.let { list.add(GitChangedFile.fromJson(it)) }
                }
            }
            return GitStatusResult(
                repo = GitRepoInfo.fromJson(o.optJSONObject("repo")),
                files = list
            )
        }
    }
}

data class GitDiffResult(
    val file: String,
    val staged: Boolean,
    val untracked: Boolean,
    val binary: Boolean,
    val truncated: Boolean,
    val added: Int,
    val deleted: Int,
    val diff: String
) {
    companion object {
        fun fromJson(o: JSONObject): GitDiffResult = GitDiffResult(
            file = o.optString("file"),
            staged = o.optBoolean("staged", false),
            untracked = o.optBoolean("untracked", false),
            binary = o.optBoolean("binary", false),
            truncated = o.optBoolean("truncated", false),
            added = o.optInt("added", 0),
            deleted = o.optInt("deleted", 0),
            diff = o.optString("diff")
        )
    }
}

data class GitCommitResult(
    val sha: String,
    val shortSha: String,
    val summary: String,
    val output: String
) {
    companion object {
        fun fromJson(o: JSONObject): GitCommitResult = GitCommitResult(
            sha = o.optString("sha"),
            shortSha = o.optString("short_sha"),
            summary = o.optString("summary"),
            output = o.optString("output")
        )
    }
}

data class GitPushResult(
    val branch: String,
    val remote: String,
    val output: String,
    val protected: Boolean
) {
    companion object {
        fun fromJson(o: JSONObject): GitPushResult = GitPushResult(
            branch = o.optString("branch"),
            remote = o.optString("remote"),
            output = o.optString("output"),
            protected = o.optBoolean("protected", false)
        )
    }
}

data class GitLogEntry(
    val sha: String,
    val shortSha: String,
    val author: String,
    val date: String,
    val subject: String
) {
    companion object {
        fun fromJson(o: JSONObject): GitLogEntry = GitLogEntry(
            sha = o.optString("sha"),
            shortSha = o.optString("short_sha"),
            author = o.optString("author"),
            date = o.optString("date"),
            subject = o.optString("subject")
        )
    }
}

fun agentDisplayName(agent: String): String = when (agent.lowercase()) {
    "claude" -> "Claude Code"
    "codex" -> "Codex"
    "agy" -> "Antigravity"
    else -> agent.ifBlank { "AI" }
}

fun formatFileSize(bytes: Long): String {
    if (bytes < 1024) return "${bytes} B"
    val kb = bytes / 1024.0
    if (kb < 1024) return String.format("%.1f KB", kb)
    val mb = kb / 1024.0
    if (mb < 1024) return String.format("%.1f MB", mb)
    return String.format("%.2f GB", mb / 1024.0)
}
