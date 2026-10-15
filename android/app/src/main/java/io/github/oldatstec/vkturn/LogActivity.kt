package io.github.oldatstec.vkturn

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.widget.Button
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast

/**
 * The daemon's log on a screen of its own. On the main screen it was part of
 * the page: redrawn every 100 ms with its oldest lines cut off the top, the
 * text under the finger kept moving and the whole form was laid out anew
 * (the user's report, 2026-10-15). Here it is redrawn at most once a second,
 * and only while the reader is at the end: scrolled up to read, nothing moves.
 */
class LogActivity : Activity() {
    private lateinit var scroll: ScrollView
    private lateinit var log: TextView
    private lateinit var paused: TextView
    private val main = Handler(Looper.getMainLooper())
    private var shown = ""
    private var scheduled = false

    private val tick = Runnable {
        scheduled = false
        refresh()
    }
    private val changed: () -> Unit = {
        if (!scheduled) {
            scheduled = true
            main.postDelayed(tick, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_log)
        scroll = findViewById(R.id.scroll)
        log = findViewById(R.id.log)
        paused = findViewById(R.id.paused)
        findViewById<Button>(R.id.copy_log).setOnClickListener {
            getSystemService(ClipboardManager::class.java)
                .setPrimaryClip(ClipData.newPlainText("Журнал VK TURN", Tunnel.logText()))
            Toast.makeText(this, "Скопировано", Toast.LENGTH_SHORT).show()
        }
        // Back at the end: catch up at once.
        scroll.setOnScrollChangeListener { _, _, _, _, _ -> if (atEnd() && shown != Tunnel.logText()) refresh() }
    }

    override fun onResume() {
        super.onResume()
        Tunnel.listen(changed)
        refresh(force = true)
    }

    override fun onPause() {
        Tunnel.unlisten(changed)
        main.removeCallbacks(tick)
        scheduled = false
        super.onPause()
    }

    private fun atEnd(): Boolean {
        val content = scroll.getChildAt(0) ?: return true
        val slack = (48 * resources.displayMetrics.density).toInt()
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
        log.text = t
        scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
    }
}
