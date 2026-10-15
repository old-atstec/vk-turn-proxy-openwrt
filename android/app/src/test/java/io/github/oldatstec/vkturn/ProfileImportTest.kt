package io.github.oldatstec.vkturn

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class ProfileImportTest {
    /** Made by the server panel itself (vkturn-panel, made-up keys and links): the two sides agree. */
    private val panelLink = "vkturn://import?c=eyJ2IjoxLCJuYW1lIjoi0KLQtdC70LXRhNC-0L0iLCJwZWVyIjoiMjAzLjAuMTEzLjEwOjU2MDA0Iiwic2VydmVyX2tleSI6IkFRRUJBUUVCQVFFQkFRRUJBUUVCQVFFQkFRRUJBUUVCQVFFQkFRRUJBUUU9IiwicHJpdmF0ZV9rZXkiOiJBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFFPSIsInBzayI6IkFnSUNBZ0lDQWdJQ0FnSUNBZ0lDQWdJQ0FnSUNBZ0lDQWdJQ0FnSUNBZ0k9IiwiYWRkcmVzcyI6IjEwLjY2LjY2LjIvMjQiLCJkbnMiOiIxLjEuMS4xIiwibXR1IjoxMjgwLCJjb25ucyI6MjAsInRyYW5zcG9ydCI6InVkcCIsInZrX2xpbmtzIjpbImh0dHBzOi8vdmsuY29tL2NhbGwvam9pbi9BQUFBZXhhbXBsZSIsImh0dHBzOi8vdmsuY29tL2NhbGwvam9pbi9CQkJCZXhhbXBsZSJdfQ"

    private val mine = Settings(
        routes = "10.0.0.0/8",
        split = SplitMode.ONLY,
        apps = setOf("org.example.browser"),
        fallbackDns = "198.51.100.53",
        tcp = true,
    )

    @Test
    fun importsThePanelsLink() {
        val imp = ProfileImport.parse(panelLink, mine)
        val s = imp.settings
        assertEquals("Телефон", imp.name)
        assertEquals("203.0.113.10:56004", s.peer)
        assertEquals("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", s.serverKey)
        assertEquals("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE=", s.privateKey)
        assertEquals("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=", s.psk)
        assertEquals("10.66.66.2/24", s.address)
        assertEquals(listOf("https://vk.com/call/join/AAAAexample", "https://vk.com/call/join/BBBBexample"), s.links)
        assertEquals("1.1.1.1", s.tunnelDns)
        assertEquals(20, s.conns)
        assertEquals(1280, s.mtu)
        assertFalse("transport udp", s.tcp)
        // What the profile does not carry stays the user's.
        assertEquals(mine.routes, s.routes)
        assertEquals(mine.split, s.split)
        assertEquals(mine.apps, s.apps)
        assertEquals(mine.fallbackDns, s.fallbackDns)
    }

    @Test
    fun findsTheLinkInAMessage() {
        val s = ProfileImport.parse("Вот подключение:\n$panelLink\nудачи", mine).settings
        assertEquals("203.0.113.10:56004", s.peer)
    }

    @Test
    fun importsTheFile() {
        val json = """
            {"v": 1, "peer": "203.0.113.10:56004", "server_key": "k", "private_key": "p",
             "address": "10.66.66.9/24", "transport": "tcp", "vk_links": ["https://vk.com/call/join/x"]}
        """
        val s = ProfileImport.parse(json, Settings()).settings
        assertEquals("10.66.66.9/24", s.address)
        assertTrue(s.tcp)
        assertEquals("", s.psk)
        assertEquals(Settings().mtu, s.mtu)
    }

    @Test
    fun refusesWhatIsNotAProfile() {
        for (bad in listOf(
            "",
            "https://vk.com/call/join/x",
            "vkturn://import?c=!!!",
            "vkturn://import?c=bm90IGpzb24", // "not json"
            """{"v": 2, "peer": "a:1"}""",
            """{"v": 1, "peer": "a:1", "server_key": "k", "private_key": "p", "address": "10.0.0.2/24", "vk_links": []}""",
            """{"v": 1, "server_key": "k", "private_key": "p", "address": "10.0.0.2/24", "vk_links": ["x"]}""",
        )) {
            try {
                ProfileImport.parse(bad, Settings())
                fail("accepted: $bad")
            } catch (e: IllegalArgumentException) {
                assertTrue(bad, !e.message.isNullOrEmpty())
            }
        }
    }
}
