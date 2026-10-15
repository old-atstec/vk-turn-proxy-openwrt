package io.github.oldatstec.vkturn

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CidrTest {
    private fun c(s: String) = Cidr.parse(s)!!

    /** Every address of a route set, counted: the subtraction loses nothing and keeps nothing extra. */
    private fun size(l: List<Cidr>) = l.sumOf { 1L shl (32 - it.prefix) }

    @Test
    fun parses() {
        assertEquals("10.66.66.2/24", c("10.66.66.2/24").toString())
        assertEquals("10.66.66.0/24", c("10.66.66.2/24").network().toString())
        assertEquals(32, c("1.1.1.1").prefix)
        for (bad in listOf("10.66.66/24", "10.66.66.256/24", "10.66.66.1/33", "a.b.c.d", "", "1.1.1.1/")) {
            assertNull(bad, Cidr.parse(bad))
        }
    }

    @Test
    fun theLocalNetworksAreTakenOutOfTheDefault() {
        val r = Cidr.subtract(listOf(c("0.0.0.0/0")), Cidr.LOCAL)
        assertEquals((1L shl 32) - size(Cidr.LOCAL), size(r))
        for (l in Cidr.LOCAL) assertTrue(r.none { it.overlaps(l) })
        assertTrue(r.any { it.contains(c("8.8.8.8/32")) })
    }

    // The emulator's finding: with the LAN bypass the server's tunnel address
    // (10.66.66.1, inside 10/8) went past the tunnel.
    @Test
    fun theTunnelsOwnSubnetStaysInTheTunnel() {
        val s = Settings(address = "10.66.66.3/24", routes = "0.0.0.0/0", bypassLan = true)
        val r = s.effectiveRoutes()
        assertTrue(r.any { it.contains(c("10.66.66.1/32")) })
        assertTrue(r.none { it.contains(c("10.0.0.1/32")) || it.contains(c("192.168.1.1/32")) })
        assertEquals((1L shl 32) - size(Cidr.LOCAL) + 256, size(r))
    }

    @Test
    fun withoutTheBypassTheRoutesAreTheUsers() {
        val s = Settings(address = "10.66.66.3/24", routes = "0.0.0.0/0", bypassLan = false)
        assertEquals(listOf(c("0.0.0.0/0")), s.effectiveRoutes())
    }
}
