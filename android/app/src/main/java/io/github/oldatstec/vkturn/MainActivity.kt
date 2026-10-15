package io.github.oldatstec.vkturn

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import org.json.JSONObject
import kotlin.concurrent.thread

class MainActivity : Activity() {
    companion object {
        private const val REQ_VPN = 1
        private const val REQ_SCAN = 2
        private const val REQ_FILE = 3
        private const val MAX_FILE = 64 * 1024
    }

    private lateinit var files: Files
    private val v = mutableMapOf<Int, View>()
    private inline fun <reified T : View> f(id: Int): T = v.getOrPut(id) { findViewById(id) } as T

    private val redraw: () -> Unit = { render() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        files = Files(this)
        fill(Settings.load(this))

        f<Button>(R.id.toggle).setOnClickListener { toggle() }
        f<Button>(R.id.genkey).setOnClickListener { newKey() }
        f<Button>(R.id.copy_pub).setOnClickListener {
            val k = f<TextView>(R.id.public_key).tag as? String
            if (k != null) copy("Публичный ключ", k) else toast("Сначала задайте приватный ключ")
        }
        f<Button>(R.id.choose_apps).setOnClickListener {
            save()
            startActivity(Intent(this, AppsActivity::class.java))
        }
        f<Button>(R.id.open_log).setOnClickListener { startActivity(Intent(this, LogActivity::class.java)) }
        f<Button>(R.id.import_profile).setOnClickListener { chooseImport() }
        f<Button>(R.id.captcha).setOnClickListener {
            val url = Tunnel.status?.optString("captcha_url").orEmpty()
            if (url.startsWith("https://")) startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
        }
        f<RadioGroup>(R.id.split).setOnCheckedChangeListener { _, _ -> renderApps() }

        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 0)
        }
        if (savedInstanceState == null) handleIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIntent(intent)
    }

    override fun onResume() {
        super.onResume()
        // The app list is edited on its own screen.
        val apps = Settings.load(this).apps
        f<TextView>(R.id.apps_summary).tag = apps
        renderApps()
        showPublicKey(f<EditText>(R.id.private_key).text.toString().trim())
        Tunnel.listen(redraw)
        render()
    }

    override fun onPause() {
        Tunnel.unlisten(redraw)
        save()
        super.onPause()
    }

    // --- the form ---

    private fun fill(s: Settings) {
        f<EditText>(R.id.peer).setText(s.peer)
        f<EditText>(R.id.server_key).setText(s.serverKey)
        f<EditText>(R.id.private_key).setText(s.privateKey)
        f<EditText>(R.id.psk).setText(s.psk)
        f<EditText>(R.id.links).setText(s.links.joinToString("\n"))
        f<EditText>(R.id.conns).setText(s.conns.toString())
        f<CheckBox>(R.id.tcp).isChecked = s.tcp
        f<EditText>(R.id.fallback_dns).setText(s.fallbackDns)
        f<EditText>(R.id.address).setText(s.address)
        f<EditText>(R.id.tunnel_dns).setText(s.tunnelDns)
        f<EditText>(R.id.mtu).setText(s.mtu.toString())
        f<EditText>(R.id.routes).setText(s.routes)
        f<CheckBox>(R.id.bypass_lan).isChecked = s.bypassLan
        f<RadioGroup>(R.id.split).check(
            when (s.split) {
                SplitMode.ALL -> R.id.split_all
                SplitMode.ONLY -> R.id.split_only
                SplitMode.EXCEPT -> R.id.split_except
            },
        )
    }

    private fun text(id: Int) = f<EditText>(id).text.toString().trim()

    private fun read(): Settings = Settings(
        peer = text(R.id.peer),
        serverKey = text(R.id.server_key),
        privateKey = text(R.id.private_key),
        psk = text(R.id.psk),
        links = Settings.parseLinks(text(R.id.links)),
        conns = text(R.id.conns).toIntOrNull() ?: 0,
        tcp = f<CheckBox>(R.id.tcp).isChecked,
        fallbackDns = text(R.id.fallback_dns),
        address = text(R.id.address),
        tunnelDns = text(R.id.tunnel_dns),
        mtu = text(R.id.mtu).toIntOrNull() ?: 0,
        routes = text(R.id.routes),
        bypassLan = f<CheckBox>(R.id.bypass_lan).isChecked,
        split = when (f<RadioGroup>(R.id.split).checkedRadioButtonId) {
            R.id.split_only -> SplitMode.ONLY
            R.id.split_except -> SplitMode.EXCEPT
            else -> SplitMode.ALL
        },
        apps = Settings.load(this).apps,
    )

    private fun save(): Settings = read().also { it.save(this) }

    @Suppress("UNCHECKED_CAST")
    private fun renderApps() {
        val apps = f<TextView>(R.id.apps_summary).tag as? Set<String> ?: emptySet()
        val mode = f<RadioGroup>(R.id.split).checkedRadioButtonId
        f<Button>(R.id.choose_apps).isEnabled = mode != R.id.split_all
        f<TextView>(R.id.apps_summary).text = when (mode) {
            R.id.split_only -> "В туннель идут только выбранные: ${apps.size}"
            R.id.split_except -> "Мимо туннеля идут выбранные: ${apps.size}"
            else -> "В туннель идут все приложения"
        }
    }

    // --- keys ---

    private fun newKey() {
        if (text(R.id.private_key).isNotEmpty()) {
            AlertDialog.Builder(this)
                .setMessage("Заменить приватный ключ? Новый публичный ключ надо будет прописать на сервере.")
                .setPositiveButton("Заменить") { _, _ -> genKey() }
                .setNegativeButton("Отмена", null)
                .show()
        } else {
            genKey()
        }
    }

    private fun genKey() = thread {
        val k = runDaemon(listOf("genkey"), null)
        runOnUiThread {
            if (k == null || !Settings.isKey(k)) return@runOnUiThread toast("Не удалось создать ключ")
            f<EditText>(R.id.private_key).setText(k)
            save()
            showPublicKey(k)
        }
    }

    private fun showPublicKey(priv: String) {
        val tv = f<TextView>(R.id.public_key)
        if (!Settings.isKey(priv)) {
            tv.text = ""
            tv.tag = null
            return
        }
        thread {
            val pub = runDaemon(listOf("pubkey"), priv)
            runOnUiThread {
                tv.tag = pub
                tv.text = if (pub != null) "Публичный ключ клиента (для сервера):\n$pub" else ""
            }
        }
    }

    /** The daemon's genkey / pubkey: the same code as the router's. */
    private fun runDaemon(args: List<String>, input: String?): String? = try {
        val p = ProcessBuilder(listOf(files.daemon.path) + args).start()
        p.outputStream.use { o -> input?.let { o.write((it + "\n").toByteArray()) } }
        val out = p.inputStream.bufferedReader().readText().trim()
        if (p.waitFor() == 0) out else null
    } catch (e: Exception) {
        null
    }

    // --- on / off ---

    private fun toggle() {
        when (Tunnel.phase) {
            Tunnel.Phase.OFF -> {
                val s = save()
                s.validate()?.let { return toast(it) }
                val ask = VpnService.prepare(this)
                if (ask != null) startActivityForResult(ask, REQ_VPN) else VkTurnService.start(this)
            }
            Tunnel.Phase.STOPPING -> {}
            else -> VkTurnService.stop(this)
        }
    }

    @Deprecated("the framework Activity's")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        when (requestCode) {
            REQ_VPN ->
                if (resultCode == RESULT_OK) VkTurnService.start(this)
                else toast("Без разрешения на VPN туннель не поднять")
            REQ_SCAN -> data?.getStringExtra(ScanActivity.EXTRA_TEXT)?.takeIf { resultCode == RESULT_OK }?.let { offerImport(it) }
            REQ_FILE -> data?.data?.takeIf { resultCode == RESULT_OK }?.let { importUri(it) }
        }
    }

    // --- import: the server panel's QR code, link or .vkturn file (ProfileImport) ---

    private fun chooseImport() {
        AlertDialog.Builder(this)
            .setTitle(R.string.import_profile)
            .setItems(arrayOf("Сканировать QR-код", "Вставить ссылку из буфера", "Открыть файл .vkturn")) { _, which ->
                when (which) {
                    0 -> startActivityForResult(Intent(this, ScanActivity::class.java), REQ_SCAN)
                    1 -> {
                        val clip = getSystemService(ClipboardManager::class.java).primaryClip
                        val text = clip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString()
                        if (text.isNullOrBlank()) toast("Буфер обмена пуст — скопируйте ссылку vkturn:// из панели") else offerImport(text)
                    }
                    2 -> startActivityForResult(
                        Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*"),
                        REQ_FILE,
                    )
                }
            }
            .show()
    }

    /** A vkturn:// link opened from elsewhere (a messenger, the camera app), a shared text or file. */
    private fun handleIntent(i: Intent?) {
        when (i?.action) {
            Intent.ACTION_VIEW -> {
                val u = i.data ?: return
                if (u.scheme == "vkturn") offerImport(u.toString()) else importUri(u)
            }
            Intent.ACTION_SEND -> {
                i.getStringExtra(Intent.EXTRA_TEXT)?.let { return offerImport(it) }
                @Suppress("DEPRECATION")
                (i.getParcelableExtra(Intent.EXTRA_STREAM) as? Uri)?.let { importUri(it) }
            }
            else -> return
        }
        // Handled once: a rotation must not offer it again.
        setIntent(Intent(this, MainActivity::class.java))
    }

    private fun importUri(u: Uri) = thread {
        val text = try {
            contentResolver.openInputStream(u)?.use { s ->
                // readNBytes is API 33: by hand, and never more than a profile could be.
                val out = java.io.ByteArrayOutputStream()
                val buf = ByteArray(8192)
                while (out.size() <= MAX_FILE) {
                    val n = s.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                }
                if (out.size() > MAX_FILE) null else out.toString("UTF-8")
            }
        } catch (e: Exception) {
            null
        }
        runOnUiThread { if (text == null) toast("Не удалось прочитать файл") else offerImport(text) }
    }

    private fun offerImport(text: String) {
        val imp = try {
            ProfileImport.parse(text, read())
        } catch (e: IllegalArgumentException) {
            return toast(e.message ?: "Не удалось импортировать")
        }
        imp.settings.validate()?.let { return toast("Подключение с ошибкой: $it") }
        val running = Tunnel.phase != Tunnel.Phase.OFF
        val msg = buildString {
            append("Сервер: ${imp.settings.peer}\n")
            append("Адрес в туннеле: ${imp.settings.address}\n")
            append("Ссылок на звонки: ${imp.settings.links.size}\n\n")
            append("Сервер, ключи и ссылки будут заменены. Маршруты и выбор приложений останутся прежними.")
            if (running) append("\n\nТуннель сейчас включён — он переподключится.")
        }
        AlertDialog.Builder(this)
            .setTitle(if (imp.name.isNotEmpty()) "Импорт «${imp.name}»" else "Импорт подключения")
            .setMessage(msg)
            .setPositiveButton(if (running) "Переподключить" else "Подключить") { _, _ -> applyImport(imp.settings, connect = true) }
            .setNeutralButton("Только сохранить") { _, _ -> applyImport(imp.settings, connect = false) }
            .setNegativeButton("Отмена", null)
            .show()
    }

    private fun applyImport(s: Settings, connect: Boolean) {
        s.save(this)
        fill(s)
        showPublicKey(s.privateKey)
        f<android.widget.ScrollView>(R.id.scroll).smoothScrollTo(0, 0)
        if (!connect) return toast("Подключение сохранено")
        if (Tunnel.phase != Tunnel.Phase.OFF) {
            // The daemon reads its config once: off, then on with the new one.
            VkTurnService.stop(this)
            Tunnel.listen(object : () -> Unit {
                override fun invoke() {
                    if (Tunnel.phase != Tunnel.Phase.OFF) return
                    Tunnel.unlisten(this)
                    runOnUiThread { toggle() }
                }
            })
        } else {
            toggle()
        }
    }

    // --- state ---

    /** Every status poll and log line lands here: a text set to itself would still lay the page out anew. */
    private fun TextView.show(s: CharSequence) {
        if (text.toString() != s.toString()) text = s
    }

    private fun render() {
        val st = Tunnel.status
        f<TextView>(R.id.state).show(when (Tunnel.phase) {
            Tunnel.Phase.OFF -> if (Tunnel.error != null) "Отключено — ошибка" else "Отключено"
            Tunnel.Phase.STARTING -> stateWord(st) ?: "Запуск…"
            Tunnel.Phase.RUNNING -> stateWord(st) ?: "Работает"
            Tunnel.Phase.STOPPING -> "Отключение…"
        })
        f<Button>(R.id.toggle).apply {
            show(getString(if (Tunnel.phase == Tunnel.Phase.OFF) R.string.connect else R.string.disconnect))
            isEnabled = Tunnel.phase != Tunnel.Phase.STOPPING
        }
        f<TextView>(R.id.details).show(details(st))
        val captcha = if (st?.optBoolean("captcha_needed") == true && st.optString("captcha_url").startsWith("https://")) View.VISIBLE else View.GONE
        if (f<Button>(R.id.captcha).visibility != captcha) f<Button>(R.id.captcha).visibility = captcha
    }

    private fun stateWord(st: JSONObject?): String? = when (st?.optString("state")) {
        "starting", "connecting" -> "Подключение к релеям VK…"
        "handshake" -> "Рукопожатие WireGuard…"
        "up" -> "Туннель работает"
        "down" -> "Нет связи — переподключение"
        "stopping" -> "Отключение…"
        else -> null
    }

    private fun details(st: JSONObject?): String {
        val lines = mutableListOf<String>()
        Tunnel.error?.let { lines += it }
        if (st == null) return lines.joinToString("\n")
        lines += "Соединений: ${st.optInt("conns_active")} из ${st.optInt("conns_configured")}" +
            " · релеев: ${st.optInt("pool_relays")} · RTT ${st.optDouble("turn_rtt_ms", 0.0).toInt()} мс"
        lines += "Принято ${mb(st.optLong("wg_rx_bytes"))} · отправлено ${mb(st.optLong("wg_tx_bytes"))}"
        val hs = st.optLong("wg_last_handshake")
        if (hs > 0) lines += "Рукопожатие WireGuard: ${System.currentTimeMillis() / 1000 - hs} с назад"
        if (st.optBoolean("captcha_needed")) lines += "VK просит капчу — автоматический решатель не прошёл"
        st.optString("last_error").takeIf { it.isNotEmpty() }?.let { lines += it }
        st.optString("auth_error").takeIf { it.isNotEmpty() }?.let { lines += it }
        return lines.joinToString("\n")
    }

    private fun mb(b: Long) = "%.1f МБ".format(b / 1e6)

    private fun copy(label: String, text: String) {
        getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText(label, text))
        toast("Скопировано")
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_LONG).show()
}
