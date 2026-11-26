package com.phoneaiaudit.app.data

import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

data class DownloadedFile(
    val uri: Uri,
    val displayName: String,
    val relativePath: String,
    val mimeType: String,
    val sizeBytes: Long
)

data class DownloadTaskState(
    val remotePath: String,
    val fileName: String,
    val bytesRead: Long = 0L,
    val totalBytes: Long = -1L,
    val finished: DownloadedFile? = null,
    val error: String? = null
) {
    val isDownloading: Boolean get() = finished == null && error == null
    val progress: Float?
        get() = if (totalBytes > 0L) (bytesRead.toFloat() / totalBytes.toFloat()).coerceIn(0f, 1f) else null
}

object FileDownloadManager {
    private const val CHUNK_SIZE_BYTES = 2L * 1024L * 1024L // 2 MB per HTTP Range request
    private const val SUBDIR = "PhoneAiAudit"

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private val downloadClient: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(45, TimeUnit.SECONDS)
        .writeTimeout(15, TimeUnit.SECONDS)
        .build()

    private val _state = MutableStateFlow<DownloadTaskState?>(null)
    val state: StateFlow<DownloadTaskState?> = _state.asStateFlow()

    @Volatile
    private var currentJob: Job? = null

    fun startDownload(context: Context, remotePath: String, fileName: String = remotePath.substringAfterLast('/')) {
        val appCtx = context.applicationContext
        val cleanName = fileName.trim().ifBlank { "download.bin" }
        currentJob?.cancel()
        _state.value = DownloadTaskState(remotePath = remotePath, fileName = cleanName)

        currentJob = scope.launch {
            try {
                val downloaded = executeChunkedDownload(appCtx, remotePath, cleanName)
                _state.value = DownloadTaskState(
                    remotePath = remotePath,
                    fileName = cleanName,
                    bytesRead = downloaded.sizeBytes,
                    totalBytes = downloaded.sizeBytes,
                    finished = downloaded
                )
            } catch (_: CancellationException) {
                if (_state.value?.remotePath == remotePath) {
                    _state.value = null
                }
            } catch (e: Exception) {
                _state.value = DownloadTaskState(
                    remotePath = remotePath,
                    fileName = cleanName,
                    bytesRead = _state.value?.bytesRead ?: 0L,
                    totalBytes = _state.value?.totalBytes ?: -1L,
                    error = e.message ?: "下载失败"
                )
            }
        }
    }

    fun cancelDownload() {
        currentJob?.cancel()
        currentJob = null
        _state.value = null
    }

    fun dismiss() {
        if (_state.value?.isDownloading != true) {
            _state.value = null
        }
    }

    fun openDownloadedFile(context: Context, file: DownloadedFile): Result<Unit> = runCatching {
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(file.uri, file.mimeType)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        try {
            context.startActivity(Intent.createChooser(intent, "打开 ${file.displayName}").apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            })
        } catch (_: Exception) {
            val fallback = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(file.uri, "*/*")
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            context.startActivity(Intent.createChooser(fallback, "打开 ${file.displayName}").apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            })
        }
    }

    fun shareDownloadedFile(context: Context, file: DownloadedFile): Result<Unit> = runCatching {
        val intent = Intent(Intent.ACTION_SEND).apply {
            type = file.mimeType
            putExtra(Intent.EXTRA_STREAM, file.uri)
            putExtra(Intent.EXTRA_SUBJECT, file.displayName)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        context.startActivity(Intent.createChooser(intent, "分享 ${file.displayName}").apply {
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        })
    }

    private suspend fun executeChunkedDownload(
        context: Context,
        remotePath: String,
        fileName: String
    ): DownloadedFile {
        val url = AuditRepository.buildStreamUrl(remotePath, kind = "raw")
        val token = AuditRepository.config.appToken
        val mimeType = inferMimeType(fileName)
        val relPath = "${Environment.DIRECTORY_DOWNLOADS}/$SUBDIR"

        val resolver = context.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.Downloads.DISPLAY_NAME, fileName)
            put(MediaStore.Downloads.MIME_TYPE, mimeType)
            put(MediaStore.Downloads.RELATIVE_PATH, relPath)
            put(MediaStore.Downloads.IS_PENDING, 1)
        }
        val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: throw IOException("无法在系统下载目录创建文件")

        var completed = false
        try {
            var written = 0L
            var totalSize = -1L

            resolver.openOutputStream(uri, "w")?.use { out ->
                val buf = ByteArray(32 * 1024)
                var offset = 0L
                var done = false

                while (!done) {
                    currentCoroutineContext().ensureActive()
                    val reqEnd = if (totalSize > 0L) {
                        minOf(offset + CHUNK_SIZE_BYTES - 1L, totalSize - 1L)
                    } else {
                        offset + CHUNK_SIZE_BYTES - 1L
                    }

                    val chunkResult = fetchChunkWithRetry(url, token, offset, reqEnd) { stream, contentLen, parsedTotal, isPartial ->
                        if (parsedTotal > 0L) {
                            totalSize = parsedTotal
                        } else if (!isPartial && contentLen >= 0L) {
                            totalSize = contentLen
                        }
                        var lastUiEmit = 0L
                        var chunkBytes = 0L
                        while (true) {
                            currentCoroutineContext().ensureActive()
                            val n = stream.read(buf)
                            if (n < 0) break
                            if (n > 0) {
                                out.write(buf, 0, n)
                                chunkBytes += n
                                written += n
                                val now = System.currentTimeMillis()
                                if (now - lastUiEmit >= 120L) {
                                    lastUiEmit = now
                                    _state.value = DownloadTaskState(
                                        remotePath = remotePath,
                                        fileName = fileName,
                                        bytesRead = written,
                                        totalBytes = totalSize
                                    )
                                }
                            }
                        }
                        _state.value = DownloadTaskState(
                            remotePath = remotePath,
                            fileName = fileName,
                            bytesRead = written,
                            totalBytes = totalSize
                        )
                        ChunkOutcome(chunkBytes = chunkBytes, isPartial = isPartial, totalSize = totalSize)
                    }

                    if (!chunkResult.isPartial) {
                        // Server returned full 200 OK body in one pass (including 0-byte empty file)
                        done = true
                    } else {
                        offset += chunkResult.chunkBytes
                        if (chunkResult.totalSize > 0L && offset >= chunkResult.totalSize) {
                            done = true
                        } else if (chunkResult.chunkBytes == 0L) {
                            done = true
                        }
                    }
                }
                out.flush()
            } ?: throw IOException("无法写入下载目标文件")

            val finalizeValues = ContentValues().apply {
                put(MediaStore.Downloads.IS_PENDING, 0)
            }
            resolver.update(uri, finalizeValues, null, null)
            completed = true

            return DownloadedFile(
                uri = uri,
                displayName = fileName,
                relativePath = "$relPath/$fileName",
                mimeType = mimeType,
                sizeBytes = written
            )
        } finally {
            if (!completed) {
                runCatching { resolver.delete(uri, null, null) }
            }
        }
    }

    private data class ChunkOutcome(
        val chunkBytes: Long,
        val isPartial: Boolean,
        val totalSize: Long
    )

    private suspend fun fetchChunkWithRetry(
        url: String,
        token: String,
        start: Long,
        end: Long,
        consumer: suspend (java.io.InputStream, Long, Long, Boolean) -> ChunkOutcome
    ): ChunkOutcome {
        var lastErr: Exception? = null
        for (attempt in 0..1) {
            currentCoroutineContext().ensureActive()
            try {
                val req = Request.Builder()
                    .url(url)
                    .header("Authorization", "Bearer $token")
                    .header("Cache-Control", "no-store")
                    .header("Range", "bytes=$start-$end")
                    .get()
                    .build()
                downloadClient.newCall(req).execute().use { resp ->
                    if (!resp.isSuccessful) {
                        val errBody = resp.body?.string().orEmpty()
                        val msg = runCatching { JSONObject(errBody).optString("error") }.getOrDefault("")
                        throw IOException(msg.ifBlank { "HTTP ${resp.code}" })
                    }
                    val body = resp.body ?: throw IOException("空响应体")
                    val isPartial = resp.code == 206
                    val parsedTotal = parseTotalFromContentRange(resp.header("Content-Range"))
                    return consumer(body.byteStream(), body.contentLength(), parsedTotal, isPartial)
                }
            } catch (ce: CancellationException) {
                throw ce
            } catch (e: Exception) {
                lastErr = e
                if (attempt == 1) throw e
            }
        }
        throw lastErr ?: IOException("下载失败")
    }

    private fun parseTotalFromContentRange(header: String?): Long {
        if (header.isNullOrBlank()) return -1L
        val slash = header.lastIndexOf('/')
        if (slash < 0 || slash >= header.lastIndex) return -1L
        return header.substring(slash + 1).trim().toLongOrNull() ?: -1L
    }

    private fun inferMimeType(fileName: String): String {
        val ext = fileName.substringAfterLast('.', "").lowercase()
        return when (ext) {
            "apk" -> "application/vnd.android.package-archive"
            "pdf" -> "application/pdf"
            "png" -> "image/png"
            "jpg", "jpeg" -> "image/jpeg"
            "webp" -> "image/webp"
            "gif" -> "image/gif"
            "svg" -> "image/svg+xml"
            "mp4", "m4v" -> "video/mp4"
            "mov" -> "video/quicktime"
            "webm" -> "video/webm"
            "mp3" -> "audio/mpeg"
            "m4a" -> "audio/mp4"
            "wav" -> "audio/wav"
            "zip" -> "application/zip"
            "gz", "tgz" -> "application/gzip"
            "tar" -> "application/x-tar"
            "json" -> "application/json"
            "md", "markdown" -> "text/markdown"
            "txt", "log" -> "text/plain"
            "csv" -> "text/csv"
            "xlsx" -> "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            "docx" -> "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
            "pptx" -> "application/vnd.openxmlformats-officedocument.presentationml.presentation"
            else -> MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
        }
    }
}
