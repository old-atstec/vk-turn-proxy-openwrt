package io.github.oldatstec.vkturn

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.io.FileWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * The tunnel as the screen sees it: the service's phase, the daemon's status
 * file (cmd/vk-turn-proxyd/status.go) and the last lines of its log. One
 * process, so a plain object; every change is told on the main thread.
 */
object Tunnel {
    enum class Phase { OFF, STARTING, RUNNING, STOPPING }

    @Volatile var phase = Phase.OFF
        private set
    @Volatile var status: JSONObject? = null
        private set
    @Volatile var error: String? = null
        private set

    /** The settings the running tunnel was started with: the screen tells when the form left them. */
    @Volatile var applied: Settings? = null

    private const val LOG_LINES = 400
    private val log = ArrayDeque<String>()
    private val listeners = mutableSetOf<() -> Unit>()
    private val main = Handler(Looper.getMainLooper())

    fun setPhase(p: Phase, err: String? = error) {
        phase = p
        error = err
        if (p == Phase.OFF) status = null
        changed()
    }

    fun setStatus(s: JSONObject?) {
        status = s
        changed()
    }

    fun addLog(line: String) {
        Log.i("vkturn", line) // adb logcat -s vkturn
        synchronized(log) {
            log.addLast(line)
            while (log.size > LOG_LINES) log.removeFirst()
            toFile(line)
        }
        changed()
    }

    // --- the log on disk: it outlives a crash (CrashLog reads the previous process's) ---

    private const val FILE_MAX = 1L shl 20
    private var logFile: File? = null
    private var writer: FileWriter? = null
    private val stamp = SimpleDateFormat("MM-dd HH:mm:ss.SSS", Locale.US)

    private fun logDir(ctx: Context) = File(ctx.filesDir, "logs").apply { mkdirs() }
    fun previousLogFile(ctx: Context) = File(logDir(ctx), "previous.log")
    fun currentLogFile(ctx: Context) = File(logDir(ctx), "current.log")

    /** At the process's start: the last process's log becomes "previous". */
    fun attachLogFile(ctx: Context) = synchronized(log) {
        val cur = currentLogFile(ctx)
        if (cur.exists()) cur.renameTo(previousLogFile(ctx))
        logFile = cur
    }

    private fun toFile(line: String) {
        val f = logFile ?: return
        try {
            val w = writer?.takeIf { f.length() <= FILE_MAX } ?: run {
                writer?.close()
                FileWriter(f, f.length() <= FILE_MAX).also { writer = it }
            }
            w.write(stamp.format(Date()) + " " + line + "\n")
            w.flush()
        } catch (_: Exception) {
            writer = null
        }
    }

    fun clearLog() = synchronized(log) { log.clear() }

    fun logText(): String = synchronized(log) { log.joinToString("\n") }

    fun lastLog(): String? = synchronized(log) { log.lastOrNull() }

    fun listen(l: () -> Unit) = main.post { listeners.add(l) }
    fun unlisten(l: () -> Unit) = main.post { listeners.remove(l) }

    private var pending = false
    private fun changed() = main.post {
        // Lines come in bursts: one redraw per burst.
        if (pending) return@post
        pending = true
        main.postDelayed({
            pending = false
            listeners.toList().forEach { it() }
        }, 100)
    }
}
