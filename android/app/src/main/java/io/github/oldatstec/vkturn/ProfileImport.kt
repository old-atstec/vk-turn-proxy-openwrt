package io.github.oldatstec.vkturn

import org.json.JSONException
import org.json.JSONObject

/**
 * A connection issued by the server's panel (vkturn-panel in
 * anton48/vk-turn-proxy): one JSON object, the .vkturn file — or, base64url'd,
 * the link its QR code carries:
 *
 *     vkturn://import?c=<base64url(JSON)>
 *
 *     {"v":1, "name":"…", "peer":"203.0.113.10:56004", "server_key":"…",
 *      "private_key":"…", "psk":"…", "address":"10.66.66.2/24", "dns":"1.1.1.1",
 *      "mtu":1280, "conns":20, "transport":"udp", "vk_links":["https://vk.com/call/join/…"]}
 *
 * What the profile does not carry (routes, the split tunnel, the fallback DNS)
 * stays as the user set it.
 */
object ProfileImport {
    private val LINK = Regex("""vkturn://import\?c=([A-Za-z0-9_-]+)""")

    class Imported(val name: String, val settings: Settings)

    /** A link (anywhere in the text: a message may hold more) or the file's JSON. */
    fun parse(text: String, base: Settings): Imported {
        val json = LINK.find(text)?.let { decode(it.groupValues[1]) }
            ?: text.trim().takeIf { it.startsWith("{") }
            ?: throw IllegalArgumentException("Это не ссылка vkturn:// и не файл .vkturn")
        val o = try {
            JSONObject(json)
        } catch (e: JSONException) {
            throw IllegalArgumentException("Повреждённые данные подключения")
        }
        val v = o.optInt("v", 0)
        if (v != 1) throw IllegalArgumentException("Версия подключения $v не поддерживается — обновите приложение")
        fun req(k: String) = o.optString(k).trim().ifEmpty { throw IllegalArgumentException("В подключении нет поля $k") }
        val links = o.optJSONArray("vk_links")
            ?.let { a -> (0 until a.length()).map { a.optString(it).trim() }.filter { it.isNotEmpty() } }
            .orEmpty()
        if (links.isEmpty()) throw IllegalArgumentException("В подключении нет ссылок на звонки VK")
        val s = base.copy(
            peer = req("peer"),
            serverKey = req("server_key"),
            privateKey = req("private_key"),
            psk = o.optString("psk").trim(),
            address = req("address"),
            links = links,
            tunnelDns = o.optString("dns").trim().ifEmpty { base.tunnelDns },
            mtu = o.optInt("mtu", 0).takeIf { it > 0 } ?: base.mtu,
            conns = o.optInt("conns", 0).takeIf { it > 0 } ?: base.conns,
            tcp = when (o.optString("transport")) {
                "tcp" -> true
                "udp" -> false
                else -> base.tcp
            },
        )
        return Imported(o.optString("name").trim(), s)
    }

    private fun decode(b64: String): String = try {
        String(java.util.Base64.getUrlDecoder().decode(b64), Charsets.UTF_8)
    } catch (e: IllegalArgumentException) {
        throw IllegalArgumentException("Ссылка vkturn:// повреждена — скопируйте её целиком")
    }
}
