package io.github.oldatstec.vkturn

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Why the app or its daemon stopped, kept as text reports a user can send.
 * A phone the app crashed on (a Honor, Android 12, on "Подключить" — the
 * user's report, 2026-10-16) is out of adb's reach: whatever it knows must be
 * on the Журнал tab after the next start.
 *
 *  - an uncaught exception in any thread of the app: written by the handler
 *    of App before the process dies, with the log of the moment;
 *  - what the system recorded of the previous process (ApplicationExitInfo,
 *    Android 11+): a native crash with its tombstone, an ANR with its traces,
 *    a kill for memory or by the vendor's battery saver — read at the next
 *    start, once per exit;
 *  - the daemon's own death, from the service.
 *
 * Secrets stay out: no keys, no call links, the server only as "задан".
 */
object CrashLog {
    private const val KEEP = 10
    private const val LOG_TAIL = 300
    private const val TRACE_MAX = 64 * 1024

    class Report(val file: File) {
        val time: Long get() = file.lastModified()
        val title: String get() = file.useLines { it.firstOrNull().orEmpty() }
        fun text(): String = file.readText()
    }

    private fun dir(ctx: Context) = File(ctx.filesDir, "crash").apply { mkdirs() }

    fun reports(ctx: Context): List<Report> =
        dir(ctx).listFiles { f -> f.name.endsWith(".txt") }.orEmpty()
            .sortedByDescending { it.lastModified() }.map { Report(it) }

    fun clear(ctx: Context) {
        dir(ctx).listFiles().orEmpty().forEach { it.delete() }
        seen(ctx, System.currentTimeMillis())
    }

    /** Reports the user has not opened the Журнал tab since. */
    fun unseen(ctx: Context): Int {
        val since = prefs(ctx).getLong("seen", 0)
        return reports(ctx).count { it.time > since }
    }

    fun markSeen(ctx: Context) = seen(ctx, System.currentTimeMillis())

    private fun seen(ctx: Context, t: Long) = prefs(ctx).edit().putLong("seen", t).apply()

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("crash", Context.MODE_PRIVATE)

    // --- writing ---

    /** From the uncaught-exception handler: synchronous, the process is about to die. */
    fun writeException(ctx: Context, thread: Thread, e: Throwable) {
        write(ctx, "Падение приложения: ${e.javaClass.simpleName}: ${e.message.orEmpty().take(120)}") {
            append("Поток: ${thread.name}\n\n")
            append(e.stackTraceToString())
        }
    }

    fun writeDaemonExit(ctx: Context, why: String) {
        write(ctx, "Демон остановился: $why") {}
    }

    /** Exits of earlier processes the system kept, each once. */
    fun collectExitReasons(ctx: Context) {
        if (Build.VERSION.SDK_INT < 30) return
        val am = ctx.getSystemService(ActivityManager::class.java) ?: return
        val p = prefs(ctx)
        val last = p.getLong("exit_seen", 0)
        val exits = try {
            am.getHistoricalProcessExitReasons(ctx.packageName, 0, 10)
        } catch (e: RuntimeException) {
            return
        }
        var newest = last
        for (x in exits.sortedBy { it.timestamp }) {
            if (x.timestamp <= last) continue
            newest = maxOf(newest, x.timestamp)
            val why = reasonName(x.reason) ?: continue // a normal exit or the user's swipe
            write(ctx, "Процесс завершён системой: $why", x.timestamp, withLog = false) {
                append("Причина: $why (${x.reason}), статус ${x.status}\n")
                append("Описание: ${x.description ?: "—"}\n")
                append("Важность процесса: ${x.importance}, память PSS ${x.pss / 1024} МБ, RSS ${x.rss / 1024} МБ\n")
                try {
                    x.traceInputStream?.use { s ->
                        val b = ByteArray(TRACE_MAX)
                        val n = s.read(b).coerceAtLeast(0)
                        append("\n--- трасса системы ---\n")
                        // A native crash's tombstone is a protobuf since Android 12:
                        // its readable runs are the signal, the frames, the libraries.
                        append(if (x.reason == ApplicationExitInfo.REASON_CRASH_NATIVE) printable(b, n) else String(b, 0, n, Charsets.UTF_8))
                    }
                } catch (_: Exception) {
                }
                val prev = Tunnel.previousLogFile(ctx)
                if (prev.exists()) {
                    append("\n\n--- журнал до падения ---\n")
                    append(prev.readLines().takeLast(LOG_TAIL).joinToString("\n"))
                }
            }
        }
        if (newest != last) p.edit().putLong("exit_seen", newest).apply()
    }

    /** The runs of 4+ printable characters of a binary, one per line — what `strings` prints. */
    private fun printable(b: ByteArray, n: Int): String {
        val out = StringBuilder()
        var run = StringBuilder()
        for (i in 0 until n) {
            val c = b[i].toInt() and 0xff
            if (c in 0x20..0x7e) {
                run.append(c.toChar())
            } else {
                if (run.length >= 4) out.append(run).append('\n')
                run = StringBuilder()
            }
        }
        if (run.length >= 4) out.append(run)
        return out.toString()
    }

    private fun reasonName(r: Int): String? = when (r) {
        // REASON_CRASH: a Java crash — the handler of App wrote it already, with more.
        ApplicationExitInfo.REASON_CRASH_NATIVE -> "падение (native)"
        ApplicationExitInfo.REASON_ANR -> "не отвечает (ANR)"
        ApplicationExitInfo.REASON_LOW_MEMORY -> "нехватка памяти"
        ApplicationExitInfo.REASON_SIGNALED -> "убит сигналом"
        ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE -> "слишком много ресурсов"
        ApplicationExitInfo.REASON_INITIALIZATION_FAILURE -> "ошибка запуска"
        ApplicationExitInfo.REASON_DEPENDENCY_DIED -> "умер зависимый процесс"
        ApplicationExitInfo.REASON_OTHER -> "другое (часто — энергосбережение прошивки)"
        ApplicationExitInfo.REASON_PERMISSION_CHANGE -> "изменились разрешения"
        else -> null
    }

    private fun write(ctx: Context, title: String, at: Long = System.currentTimeMillis(), withLog: Boolean = true, body: StringBuilder.() -> Unit) {
        try {
            val sb = StringBuilder()
            sb.append(title).append('\n')
            sb.append(header(ctx, at)).append('\n')
            sb.body()
            if (withLog) {
                sb.append("\n\n--- журнал ---\n")
                sb.append(Tunnel.logText().lines().takeLast(LOG_TAIL).joinToString("\n"))
            }
            val f = File(dir(ctx), "report-" + SimpleDateFormat("yyyyMMdd-HHmmss-SSS", Locale.US).format(Date(at)) + ".txt")
            f.writeText(sb.toString())
            f.setLastModified(at)
            reports(ctx).drop(KEEP).forEach { it.file.delete() }
        } catch (_: Throwable) {
            // A report that cannot be written must not hide the crash it is about.
        }
    }

    fun header(ctx: Context, at: Long = System.currentTimeMillis()): String {
        val pi = try {
            ctx.packageManager.getPackageInfo(ctx.packageName, 0)
        } catch (e: Exception) {
            null
        }
        val s = try {
            Settings.load(ctx)
        } catch (e: Exception) {
            null
        }
        return buildString {
            append("Время: ${SimpleDateFormat("yyyy-MM-dd HH:mm:ss Z", Locale.US).format(Date(at))}\n")
            append("Приложение: ${pi?.versionName} (${pi?.longVersionCode})\n")
            append("Устройство: ${Build.MANUFACTURER} ${Build.MODEL} (${Build.DEVICE})\n")
            append("Android: ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT}), прошивка ${Build.DISPLAY}\n")
            append("ABI: ${Build.SUPPORTED_ABIS.joinToString()}\n")
            append("Состояние туннеля: ${Tunnel.phase}\n")
            if (s != null) {
                append(
                    "Настройки: сервер ${if (s.peer.isEmpty()) "не задан" else "задан"}, ссылок ${s.links.size}, " +
                        "соединений ${s.conns}, ${if (s.tcp) "tcp" else "udp"}, MTU ${s.mtu}, адрес ${s.address}, " +
                        "режим ${s.split}, приложений ${s.apps.size}, маршруты ${s.routes}, мимо локальных ${s.bypassLan}\n",
                )
            }
        }
    }
}
