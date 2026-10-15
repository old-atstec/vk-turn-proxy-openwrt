package io.github.oldatstec.vkturn

import android.os.Handler
import android.os.Looper
import org.json.JSONObject

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
        synchronized(log) {
            log.addLast(line)
            while (log.size > LOG_LINES) log.removeFirst()
        }
        changed()
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
