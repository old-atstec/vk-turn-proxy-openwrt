package io.github.oldatstec.vkturn

/** An IPv4 network: the routes of the VPN and what is taken out of them. */
data class Cidr(val addr: Long, val prefix: Int) {
    private val size: Long get() = 1L shl (32 - prefix)

    fun network(): Cidr = Cidr(addr and mask(prefix), prefix)

    private fun first() = network().addr
    private fun last() = first() + size - 1

    fun contains(o: Cidr) = prefix <= o.prefix && first() <= o.first() && o.last() <= last()
    fun overlaps(o: Cidr) = first() <= o.last() && o.first() <= last()

    fun ip(): String = listOf(24, 16, 8, 0).joinToString(".") { ((addr shr it) and 0xff).toString() }

    override fun toString() = "${ip()}/$prefix"

    companion object {
        /** What "local networks" means for the bypass: RFC 1918, link-local, multicast. */
        val LOCAL = listOf("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4")
            .map { parse(it)!! }

        private fun mask(prefix: Int): Long = if (prefix == 0) 0 else (0xffffffffL shl (32 - prefix)) and 0xffffffffL

        fun parseIp(s: String): Long? {
            val p = s.trim().split('.')
            if (p.size != 4) return null
            var v = 0L
            for (o in p) {
                val n = o.toIntOrNull() ?: return null
                if (n !in 0..255 || o.isEmpty() || o.length > 3) return null
                v = (v shl 8) or n.toLong()
            }
            return v
        }

        /** "a.b.c.d/n", or a bare address as /32. */
        fun parse(s: String): Cidr? {
            val t = s.trim()
            val slash = t.indexOf('/')
            val ip = parseIp(if (slash < 0) t else t.substring(0, slash)) ?: return null
            val prefix = if (slash < 0) 32 else t.substring(slash + 1).toIntOrNull() ?: return null
            if (prefix !in 0..32) return null
            return Cidr(ip, prefix)
        }

        /** routes minus every network of out, as the fewest disjoint networks. */
        fun subtract(routes: List<Cidr>, out: List<Cidr>): List<Cidr> {
            val res = ArrayList<Cidr>()
            fun walk(c: Cidr) {
                if (out.any { it.contains(c) }) return
                if (out.none { it.overlaps(c) }) {
                    res.add(c)
                    return
                }
                val half = c.prefix + 1
                walk(Cidr(c.addr, half))
                walk(Cidr(c.addr or (1L shl (32 - half)), half))
            }
            routes.forEach { walk(it.network()) }
            return res
        }
    }
}
