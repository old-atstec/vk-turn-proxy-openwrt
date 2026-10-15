package io.github.oldatstec.vkturn

import android.content.Context
import android.util.Base64

/** Which apps go into the tunnel: VpnService.Builder's per-app lists. */
enum class SplitMode { ALL, ONLY, EXCEPT }

/**
 * What the user set, kept in the app's private preferences. The daemon gets it
 * as its config file (key=value, 0600 — the format of the router's protocol
 * handler, cmd/vk-turn-proxyd/config.go), the VPN as the Builder's calls.
 */
data class Settings(
    val peer: String = "",
    val serverKey: String = "",
    val privateKey: String = "",
    val psk: String = "",
    val links: List<String> = emptyList(),
    val conns: Int = 20,
    val tcp: Boolean = false,
    val fallbackDns: String = "77.88.8.8",
    val address: String = "",
    val tunnelDns: String = "1.1.1.1",
    val mtu: Int = 1280,
    val routes: String = "0.0.0.0/0",
    val bypassLan: Boolean = true,
    val split: SplitMode = SplitMode.ALL,
    val apps: Set<String> = emptySet(),
) {
    /** The first thing wrong, in the user's words, or null. */
    fun validate(): String? {
        val (host, port) = splitHostPort(peer) ?: return "Адрес сервера: нужен host:port"
        if (host.isEmpty() || port !in 1..65535) return "Адрес сервера: нужен host:port"
        if (!isKey(serverKey)) return "Публичный ключ сервера: не ключ WireGuard (base64, 32 байта)"
        if (!isKey(privateKey)) return "Приватный ключ: не ключ WireGuard — нажмите «Новый ключ»"
        if (psk.isNotEmpty() && !isKey(psk)) return "Preshared key: не ключ WireGuard"
        if (links.isEmpty()) return "Нужна хотя бы одна ссылка на звонок VK"
        if (conns !in 1..120) return "Соединений: от 1 до 120"
        if (Cidr.parse(address) == null) return "Адрес в туннеле: нужен IPv4 CIDR, например 10.66.66.2/32"
        if (tunnelDnsList().any { Cidr.parseIp(it) == null }) return "DNS в туннеле: IPv4-адреса через запятую"
        if (fallbackDns.isNotEmpty() && Cidr.parseIp(fallbackDns) == null) return "DNS для запросов к VK: IPv4-адрес"
        if (mtu !in 1000..1400) return "MTU: от 1000 до 1400"
        if (routeList().isEmpty()) return "Маршруты: нужен хотя бы один IPv4 CIDR"
        if (routes.split(',', '\n').map { it.trim() }.filter { it.isNotEmpty() }.any { Cidr.parse(it) == null }) {
            return "Маршруты: IPv4 CIDR через запятую"
        }
        if (split != SplitMode.ALL && apps.isEmpty()) return "Раздельное туннелирование: не выбрано ни одного приложения"
        return null
    }

    fun tunnelDnsList(): List<String> = tunnelDns.split(',', ' ', '\n').map { it.trim() }.filter { it.isNotEmpty() }

    fun routeList(): List<Cidr> = routes.split(',', '\n').mapNotNull { Cidr.parse(it.trim()) }

    /** The routes the VPN takes: the user's, minus the local networks if asked. */
    fun effectiveRoutes(): List<Cidr> {
        val r = routeList().map { it.network() }
        return if (bypassLan) Cidr.subtract(r, Cidr.LOCAL) else r
    }

    /** The daemon's config file (cmd/vk-turn-proxyd/config.go). */
    fun daemonConfig(files: Files, dns: String): String = buildString {
        fun kv(k: String, v: Any) = append(k).append('=').append(v).append('\n')
        append("# written by the app; the daemon reads it once\n")
        kv("iface", "vkturn")
        kv("peer", peer)
        kv("private_key", privateKey)
        kv("public_key", serverKey)
        if (psk.isNotEmpty()) kv("preshared_key", psk)
        kv("mtu", mtu)
        links.forEach { kv("vk_link", it) }
        kv("conns", conns)
        kv("turn_transport", if (tcp) "tcp" else "udp")
        if (dns.isNotEmpty()) kv("dns_server", dns)
        kv("status_file", files.status.path)
        kv("cred_cache", files.credCache.path)
        kv("tun_fd_socket", files.tunSocket.path)
        kv("network_events", "stdin")
    }

    fun save(ctx: Context) {
        prefs(ctx).edit()
            .putString("peer", peer)
            .putString("server_key", serverKey)
            .putString("private_key", privateKey)
            .putString("psk", psk)
            .putString("links", links.joinToString("\n"))
            .putInt("conns", conns)
            .putBoolean("tcp", tcp)
            .putString("fallback_dns", fallbackDns)
            .putString("address", address)
            .putString("tunnel_dns", tunnelDns)
            .putInt("mtu", mtu)
            .putString("routes", routes)
            .putBoolean("bypass_lan", bypassLan)
            .putString("split", split.name)
            .putStringSet("apps", apps)
            .apply()
    }

    companion object {
        private fun prefs(ctx: Context) = ctx.getSharedPreferences("settings", Context.MODE_PRIVATE)

        fun load(ctx: Context): Settings {
            val p = prefs(ctx)
            val d = Settings()
            return Settings(
                peer = p.getString("peer", d.peer)!!,
                serverKey = p.getString("server_key", d.serverKey)!!,
                privateKey = p.getString("private_key", d.privateKey)!!,
                psk = p.getString("psk", d.psk)!!,
                links = parseLinks(p.getString("links", "")!!),
                conns = p.getInt("conns", d.conns),
                tcp = p.getBoolean("tcp", d.tcp),
                fallbackDns = p.getString("fallback_dns", d.fallbackDns)!!,
                address = p.getString("address", d.address)!!,
                tunnelDns = p.getString("tunnel_dns", d.tunnelDns)!!,
                mtu = p.getInt("mtu", d.mtu),
                routes = p.getString("routes", d.routes)!!,
                bypassLan = p.getBoolean("bypass_lan", d.bypassLan),
                split = runCatching { SplitMode.valueOf(p.getString("split", "ALL")!!) }.getOrDefault(SplitMode.ALL),
                apps = p.getStringSet("apps", emptySet())!!.toSet(),
            )
        }

        fun saveApps(ctx: Context, apps: Set<String>) {
            prefs(ctx).edit().putStringSet("apps", apps).apply()
        }

        fun parseLinks(s: String): List<String> =
            s.split('\n', ' ', ',').map { it.trim() }.filter { it.isNotEmpty() }

        fun isKey(s: String): Boolean = try {
            Base64.decode(s, Base64.NO_WRAP).size == 32 && s.trim() == s && s.length == 44
        } catch (e: IllegalArgumentException) {
            false
        }

        /** "host:port" or "[v6]:port". */
        fun splitHostPort(s: String): Pair<String, Int>? {
            val i = s.lastIndexOf(':')
            if (i <= 0) return null
            val port = s.substring(i + 1).toIntOrNull() ?: return null
            return s.substring(0, i).removePrefix("[").removeSuffix("]") to port
        }
    }
}

/** Where the app and its daemon keep their files: all in the app's own data. */
class Files(ctx: Context) {
    val daemon = java.io.File(ctx.applicationInfo.nativeLibraryDir, "libvkturnd.so")
    val config = java.io.File(ctx.filesDir, "vkturn.conf")
    // The daemon makes these two directories itself, 0700 (prepareDir).
    val status = java.io.File(ctx.filesDir, "run/status.json")
    val credCache = java.io.File(ctx.filesDir, "state/creds.json")
    val tunSocket = java.io.File(ctx.noBackupFilesDir, "tun.sock")
}
