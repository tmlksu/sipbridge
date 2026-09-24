package io.github.tmlksu.sipbridge

import java.net.InetAddress
import java.net.ServerSocket
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * RelayClient.SendBufferSocketFactory が SO_SNDBUF を設定することのテスト。
 * Linux は要求値を 2 倍して採用するため、取得値は要求値〜2 倍の範囲を許す。
 */
class SendBufferSocketFactoryTest {

    private fun assertSndbuf(requested: Int, actual: Int) {
        assertTrue("sendBufferSize=$actual (要求 $requested)", actual in requested..requested * 2)
    }

    @Test
    fun `unconnected socket gets send buffer size`() {
        // OkHttp が使う経路 (引数無し createSocket → 自分で connect)
        RelayClient.SendBufferSocketFactory().createSocket().use { s ->
            assertFalse(s.isConnected)
            assertSndbuf(RelayClient.RELAY_SOCKET_SNDBUF_BYTES, s.sendBufferSize)
        }
    }

    @Test
    fun `custom size is applied`() {
        RelayClient.SendBufferSocketFactory(sendBufferBytes = 8192).createSocket().use { s ->
            assertSndbuf(8192, s.sendBufferSize)
        }
    }

    @Test
    fun `connecting overloads also set send buffer size`() {
        val f = RelayClient.SendBufferSocketFactory()
        val loop = InetAddress.getLoopbackAddress()
        ServerSocket(0, 4, loop).use { server ->
            val port = server.localPort
            val sockets = listOf(
                f.createSocket(loop, port),
                f.createSocket(loop.hostAddress!!, port),
                f.createSocket(loop, port, loop, 0),
                f.createSocket(loop.hostAddress!!, port, loop, 0)
            )
            try {
                for (s in sockets) {
                    assertTrue(s.isConnected)
                    assertEquals(port, s.port)
                    assertSndbuf(RelayClient.RELAY_SOCKET_SNDBUF_BYTES, s.sendBufferSize)
                }
            } finally {
                sockets.forEach { runCatching { it.close() } }
            }
        }
    }
}
