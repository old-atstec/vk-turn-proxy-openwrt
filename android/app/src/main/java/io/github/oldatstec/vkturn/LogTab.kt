package io.github.oldatstec.vkturn

import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.text.format.DateFormat
import android.view.View
import android.widget.Button
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import java.util.Date

/**
 * The Журнал tab: crash reports (CrashLog) and the daemon's log.
 *
 * The log is redrawn at most once a second and only while the reader is at
 * its end: scrolled up to read, nothing moves (on the old main screen it
 * jerked the whole form — the user's report, 2026-10-15).
 */
class LogTab(private val a: Activity) {
    private val scroll: ScrollView = a.findViewById(R.id.log_scroll)
    private val log: TextView = a.findViewById(R.id.log)
    private val paused: TextView = a.findViewById(R.id.paused)
    private val crashes: TextView = a.findViewById(R.id.crashes)
    private val main = Handler(Looper.getMainLooper())
    private var shown = ""
    private var scheduled = false
    private var visible = false

    private val tick = Runnable {
        scheduled = false
        refresh()
    }
    private val changed: () -> Unit = {
        if (visible && !scheduled) {
            scheduled = true
            main.postDelayed(tick, 1000)
        }
    }

    init {
        a.findViewById<Button>(R.id.copy_log).setOnClickListener { copy("Журнал VK TURN", Tunnel.logText()) }
        a.findViewById<Button>(R.id.show_report).setOnClickListener { showLatest() }
        a.findViewById<Button>(R.id.send_report).setOnClickListener { send() }
        a.findViewById<Button>(R.id.clear_reports).setOnClickListener {
            CrashLog.clear(a)
            renderCrashes()
        }
        scroll.setOnScrollChangeListener { _, _, _, _, _ -> if (atEnd() && shown != Tunnel.logText()) refresh() }
    }

    fun show() {
        visible = true
        Tunnel.listen(changed)
        CrashLog.markSeen(a)
        renderCrashes()
        refresh(force = true)
    }

    fun hide() {
        visible = false
        Tunnel.unlisten(changed)
        main.removeCallbacks(tick)
        scheduled = false
    }

    private fun renderCrashes() {
        val r = CrashLog.reports(a)
        crashes.text = if (r.isEmpty()) {
            "Отчётов нет."
        } else {
            "Отчётов: ${r.size}. Последний — ${stamp(r[0].time)}:\n${r[0].title}"
        }
        val any = if (r.isEmpty()) View.GONE else View.VISIBLE
        a.findViewById<Button>(R.id.show_report).visibility = any
        a.findViewById<Button>(R.id.clear_reports).visibility = any
    }

    private fun stamp(t: Long) =
        DateFormat.getDateFormat(a).format(Date(t)) + " " + DateFormat.getTimeFormat(a).format(Date(t))

    private fun showLatest() {
        val r = CrashLog.reports(a).firstOrNull() ?: return
        val tv = TextView(a).apply {
            text = r.text()
            setTextIsSelectable(true)
            typeface = android.graphics.Typeface.MONOSPACE
            textSize = 11f
            val p = (16 * a.resources.displayMetrics.density).toInt()
            setPadding(p, p, p, p)
        }
        AlertDialog.Builder(a)
            .setTitle("Отчёт от ${stamp(r.time)}")
            .setView(ScrollView(a).apply { addView(tv) })
            .setPositiveButton("Отправить") { _, _ -> send() }
            .setNeutralButton("Копировать") { _, _ -> copy("Отчёт VK TURN", r.text()) }
            .setNegativeButton("Закрыть", null)
            .show()
    }

    /** The latest report, if any, and the log now — as text to a messenger or mail. */
    private fun send() {
        val text = buildString {
            CrashLog.reports(a).firstOrNull()?.let { append(it.text()).append("\n\n=====\n\n") }
            append("Текущее состояние\n").append(CrashLog.header(a))
            append("\n--- журнал ---\n").append(Tunnel.logText())
        }
        a.startActivity(
            Intent.createChooser(
                Intent(Intent.ACTION_SEND).setType("text/plain")
                    .putExtra(Intent.EXTRA_SUBJECT, "VK TURN: отчёт")
                    .putExtra(Intent.EXTRA_TEXT, text),
                "Отправить отчёт",
            ),
        )
    }

    private fun atEnd(): Boolean {
        val content = scroll.getChildAt(0) ?: return true
        val slack = (48 * a.resources.displayMetrics.density).toInt()
        return content.bottom - (scroll.height + scroll.scrollY) <= slack
    }

    private fun refresh(force: Boolean = false) {
        val t = Tunnel.logText()
        if (t == shown) {
            paused.visibility = View.INVISIBLE
            return
        }
        if (!force && !atEnd()) {
            paused.visibility = View.VISIBLE
            return
        }
        paused.visibility = View.INVISIBLE
        shown = t
        log.text = t.ifEmpty { "Журнал пуст: он заполняется при подключении." }
        scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun copy(label: String, text: String) {
        a.getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText(label, text))
        Toast.makeText(a, "Скопировано", Toast.LENGTH_SHORT).show()
    }
}
