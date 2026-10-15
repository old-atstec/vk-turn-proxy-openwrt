package io.github.oldatstec.vkturn

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.graphics.drawable.Icon
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.LocalServerSocket
import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.Process
import android.system.Os
import android.system.OsConstants
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.io.OutputStream
import java.net.Inet4Address
import java.net.Inet6Address
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

/**
 * The VPN. The daemon (cmd/vk-turn-proxyd, "the app mode" in app.go) runs as
 * this app's child process and does the tunnel — TURN, DTLS-SRTP, WireGuard;
 * this service owns what only an app can: the VPN interface and its per-app
 * list (the split tunnel), the network the phone is on, the notification.
 *
 *  1. the daemon is started with its config and connects to VK's relays over
 *     the phone's network — the app's own uid is always outside the VPN, so
 *     its sockets need no protect();
 *  2. after the first TURN session it asks for the tun over a unix socket in
 *     the app's data: only now the VPN is established and its descriptor sent
 *     (SCM_RIGHTS) — until then the phone's traffic goes as it went;
 *  3. the default network's changes and its DNS go to the daemon's stdin;
 *     closing stdin stops it (the TURN allocations are given back).
 */
class VkTurnService : VpnService() {
    companion object {
        const val ACTION_START = "io.github.oldatstec.vkturn.START"
        const val ACTION_STOP = "io.github.oldatstec.vkturn.STOP"
        private const val CHANNEL = "tunnel"
        private const val NOTE = 1

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, VkTurnService::class.java).setAction(ACTION_START))
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, VkTurnService::class.java).setAction(ACTION_STOP))
        }
    }

    private val main = Handler(Looper.getMainLooper())
    private lateinit var files: Files
    private var settings = Settings()

    private var proc: java.lang.Process? = null
    private var stdin: OutputStream? = null
    private var tun: ParcelFileDescriptor? = null
    private var listenSock: LocalSocket? = null
    private var server: LocalServerSocket? = null
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    @Volatile private var stopping = false
    private var lastState = ""

    override fun onCreate() {
        super.onCreate()
        files = Files(this)
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Туннель", NotificationManager.IMPORTANCE_LOW))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            foreground("Отключение…")
            shutdown(null)
            return START_NOT_STICKY
        }
        // ACTION_START from the screen, android.net.VpnService from always-on.
        foreground("Подключение…")
        if (proc == null && !stopping) begin()
        return START_STICKY
    }

    override fun onRevoke() {
        shutdown("VPN отключён системой или другим приложением")
    }

    override fun onDestroy() {
        if (proc != null) shutdown(null)
        super.onDestroy()
    }

    private fun begin() {
        settings = Settings.load(this)
        settings.validate()?.let { return fail(it) }
        if (prepare(this) != null) return fail("Нет разрешения на VPN — подключитесь из приложения")
        if (!files.daemon.canExecute()) return fail("В пакете нет демона для этой архитектуры (${Build.SUPPORTED_ABIS.joinToString()})")

        Tunnel.clearLog()
        Tunnel.setPhase(Tunnel.Phase.STARTING, null)
        stopping = false
        lastState = ""
        try {
            listenForTunRequest()
            writeConfig(settings.daemonConfig(files, underlyingDns()))
            startDaemon()
        } catch (e: IOException) {
            return fail("Запуск: ${e.message}")
        }
        watchNetwork()
        main.post(pollStatus)
    }

    private fun writeConfig(text: String) {
        val tmp = File(files.config.path + ".tmp")
        tmp.delete()
        tmp.outputStream().use { out ->
            Os.chmod(tmp.path, 0x180) // 0600 before a byte of the key: the daemon refuses anything wider
            out.write(text.toByteArray())
        }
        if (!tmp.renameTo(files.config)) throw IOException("cannot write ${files.config}")
    }

    private fun startDaemon() {
        val pb = ProcessBuilder(files.daemon.path, "-config", files.config.path)
            .redirectErrorStream(true)
            .directory(filesDir)
        // A Linux binary: Android's CA store is not where it looks.
        pb.environment()["SSL_CERT_DIR"] = "/system/etc/security/cacerts"
        pb.environment()["TMPDIR"] = cacheDir.path
        val p = pb.start()
        proc = p
        stdin = p.outputStream
        thread(name = "daemon-log") {
            try {
                p.inputStream.bufferedReader().forEachLine { Tunnel.addLog(it) }
            } catch (_: IOException) {
            }
            val code = p.waitFor()
            main.post { onDaemonExit(p, code) }
        }
    }

    private fun onDaemonExit(p: java.lang.Process, code: Int) {
        if (proc !== p) return
        proc = null
        if (stopping) return
        val why = if (code > 128) "убит сигналом ${code - 128}" else "код $code"
        fail("Демон остановился ($why): ${Tunnel.lastLog() ?: "—"}")
    }

    // --- the tun, on the daemon's request ---

    private fun listenForTunRequest() {
        files.tunSocket.delete()
        val ls = LocalSocket()
        ls.bind(LocalSocketAddress(files.tunSocket.path, LocalSocketAddress.Namespace.FILESYSTEM))
        val srv = LocalServerSocket(ls.fileDescriptor)
        listenSock = ls
        server = srv
        thread(name = "tun-request") {
            while (!stopping) {
                val c = try {
                    srv.accept()
                } catch (_: IOException) {
                    break
                }
                try {
                    c.use { serveTun(it) }
                } catch (e: IOException) {
                    Tunnel.addLog("app: tun: ${e.message}")
                }
            }
        }
    }

    private fun serveTun(c: LocalSocket) {
        if (c.peerCredentials.uid != Process.myUid()) return
        val pfd = tun ?: establish()?.also { tun = it }
        if (pfd != null) c.setFileDescriptorsForSend(arrayOf(pfd.fileDescriptor))
        // A byte with no descriptor is the refusal the daemon reports.
        c.outputStream.write(1)
        c.outputStream.flush()
    }

    private fun establish(): ParcelFileDescriptor? {
        val s = settings
        val b = Builder().setSession("VK TURN").setMtu(s.mtu)
        val addr = Cidr.parse(s.address)!!
        b.addAddress(addr.ip(), addr.prefix)
        s.tunnelDnsList().forEach { b.addDnsServer(it) }
        s.effectiveRoutes().forEach { b.addRoute(it.ip(), it.prefix) }
        // No IPv6 address or route: Android blocks IPv6 of the apps inside
        // rather than letting it pass by the tunnel.
        when (s.split) {
            // The daemon is this app: outside, whatever the user chose.
            SplitMode.ALL -> b.addDisallowedApplication(packageName)
            SplitMode.EXCEPT -> {
                b.addDisallowedApplication(packageName)
                s.apps.filter { it != packageName }.forEach { allow(it) { b.addDisallowedApplication(it) } }
            }
            SplitMode.ONLY -> {
                var any = false
                s.apps.filter { it != packageName }.forEach { allow(it) { b.addAllowedApplication(it); any = true } }
                if (!any) {
                    Tunnel.addLog("app: none of the chosen apps is installed")
                    return null
                }
            }
        }
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        b.setConfigureIntent(
            PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE),
        )
        return try {
            b.establish()
        } catch (e: RuntimeException) {
            Tunnel.addLog("app: VPN: ${e.message}")
            null
        }
    }

    private inline fun allow(pkg: String, add: () -> Unit) {
        try {
            add()
        } catch (_: PackageManager.NameNotFoundException) {
            Tunnel.addLog("app: $pkg is not installed — skipped")
        }
    }

    // --- the phone's network, for the daemon ---

    private fun send(line: String) {
        synchronized(this) {
            try {
                stdin?.apply {
                    write((line + "\n").toByteArray())
                    flush()
                }
            } catch (_: IOException) {
            }
        }
    }

    /** Called before the VPN is established: the active network is still the phone's. */
    private fun underlyingDns(): String {
        val cm = getSystemService(ConnectivityManager::class.java)
        return cm.activeNetwork?.let { cm.getLinkProperties(it) }?.let { pickDns(it) } ?: settings.fallbackDns
    }

    /** IPv4 first; an IPv6 resolver only if it needs no scope. */
    private fun pickDns(lp: LinkProperties): String? {
        val all = lp.dnsServers
        all.firstOrNull { it is Inet4Address }?.let { return it.hostAddress }
        return all.firstOrNull { it is Inet6Address && !it.isLinkLocalAddress }?.hostAddress?.substringBefore('%')
    }

    private fun watchNetwork() {
        val cm = getSystemService(ConnectivityManager::class.java)
        // The physical network, asked for by name: the default-network
        // callback of the VPN's own app reports the VPN once it is up, though
        // the app is outside it (seen on the emulator, Android 15) — and the
        // daemon then never heard of Wi-Fi giving way to mobile data.
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        val cb = object : ConnectivityManager.NetworkCallback() {
            private var current: Network? = null
            private var dns: String? = null

            override fun onAvailable(network: Network) {
                current = network
                Tunnel.addLog("app: the phone's network is now $network")
                send("network $network")
            }

            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) {
                if (network != current) return
                val d = pickDns(lp) ?: settings.fallbackDns.ifEmpty { null } ?: return
                if (d != dns) {
                    dns = d
                    Tunnel.addLog("app: its DNS is $d")
                    send("dns $d")
                }
            }

            override fun onLost(network: Network) {
                if (network != current) return
                current = null
                Tunnel.addLog("app: the phone has no network")
                send("network none")
            }
        }
        if (Build.VERSION.SDK_INT >= 31) {
            cm.registerBestMatchingNetworkCallback(req, cb, main)
        } else {
            cm.requestNetwork(req, cb) // the best match only: the system's own choice
        }
        netCallback = cb
    }

    // --- status, notification ---

    private val pollStatus: Runnable = object : Runnable {
        override fun run() {
            if (proc == null) return
            val st = try {
                JSONObject(files.status.readText())
            } catch (_: Exception) {
                null
            }
            if (st != null) {
                Tunnel.setStatus(st)
                if (Tunnel.phase == Tunnel.Phase.STARTING && st.optString("state") == "up") {
                    Tunnel.setPhase(Tunnel.Phase.RUNNING)
                }
                val state = st.optString("state")
                if (state != lastState) {
                    lastState = state
                    foreground(stateText(st))
                }
            }
            main.postDelayed(this, 1000)
        }
    }

    private fun stateText(st: JSONObject): String = when (st.optString("state")) {
        "starting", "connecting" -> "Подключение к релеям VK…"
        "handshake" -> "Рукопожатие WireGuard…"
        "up" -> "Туннель работает"
        "down" -> "Туннель не передаёт данные, переподключение…"
        "stopping" -> "Отключение…"
        else -> st.optString("state")
    }

    private fun foreground(text: String) {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(
            this, 1, Intent(this, VkTurnService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE,
        )
        val n = Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("VK TURN")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource(this, R.drawable.ic_notification), getString(R.string.disconnect), stop,
                ).build(),
            )
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTE, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTE, n)
        }
    }

    // --- stopping ---

    private fun fail(msg: String) {
        Tunnel.addLog("app: $msg")
        shutdown(msg)
    }

    /** Stops the daemon gracefully, then the VPN; err is shown on the screen. */
    private fun shutdown(err: String?) {
        if (stopping) return
        stopping = true
        Tunnel.setPhase(Tunnel.Phase.STOPPING, err)
        main.removeCallbacks(pollStatus)
        netCallback?.let {
            runCatching { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) }
        }
        netCallback = null
        val p = proc
        val out = stdin
        stdin = null
        thread(name = "stop") {
            // The end of stdin is the daemon's stop: it gives the allocations back.
            runCatching { out?.close() }
            if (p != null && !p.waitFor(5, TimeUnit.SECONDS)) p.destroy()
            listenSock?.let { s -> runCatching { Os.shutdown(s.fileDescriptor, OsConstants.SHUT_RDWR) } }
            runCatching { server?.close() }
            runCatching { listenSock?.close() }
            files.tunSocket.delete()
            runCatching { tun?.close() }
            main.post {
                proc = null
                tun = null
                server = null
                listenSock = null
                Tunnel.setPhase(Tunnel.Phase.OFF, err)
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                stopping = false
            }
        }
    }
}
