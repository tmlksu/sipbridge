package io.github.tmlksu.sipbridge

import java.io.CharConversionException
import java.io.IOException
import java.security.GeneralSecurityException
import java.security.KeyStoreException
import java.security.ProviderException
import java.security.UnrecoverableKeyException
import javax.crypto.AEADBadTagException
import org.junit.Assert.assertEquals
import org.junit.Test

/** #42: EncryptedSharedPreferences を開けなかった原因の分類。 */
class PrefsFailureTest {

    private fun classify(t: Throwable) = PrefsFailure.classify(t) { null }

    @Test
    fun `corrupt data or lost key is CORRUPT`() {
        assertEquals(PrefsFailure.CORRUPT, classify(AEADBadTagException("tag mismatch")))
        assertEquals(PrefsFailure.CORRUPT, classify(CharConversionException("bad utf8")))
        assertEquals(PrefsFailure.CORRUPT, classify(UnrecoverableKeyException("gone")))
        assertEquals(
            PrefsFailure.CORRUPT,
            classify(SecurityException("Could not decrypt value. decryption failed"))
        )
    }

    @Test
    fun `corrupt cause wrapped in other exceptions is CORRUPT`() {
        // CharConversionException は IOException のサブクラスだが、壊れ判定を優先する
        assertEquals(
            PrefsFailure.CORRUPT,
            classify(IOException("read", CharConversionException("bad")))
        )
        assertEquals(
            PrefsFailure.CORRUPT,
            classify(
                GeneralSecurityException(
                    "keyset", KeyStoreException("ks", AEADBadTagException("tag"))
                )
            )
        )
    }

    @Test
    fun `tink protobuf parse failure is CORRUPT`() {
        // security-crypto が依存する tink-android の shaded protobuf。見つからなければテスト失敗にする
        // (クラス名が変わったら分類表も直す必要があるため)。
        val cls = Class.forName("com.google.crypto.tink.shaded.protobuf.InvalidProtocolBufferException")
        val e = cls.getConstructor(String::class.java).newInstance("truncated") as Throwable
        assertEquals(PrefsFailure.CORRUPT, classify(RuntimeException(e)))
    }

    @Test
    fun `keystore and io hiccups are TRANSIENT`() {
        assertEquals(PrefsFailure.TRANSIENT, classify(KeyStoreException("System error")))
        assertEquals(PrefsFailure.TRANSIENT, classify(ProviderException("keystore busy")))
        assertEquals(PrefsFailure.TRANSIENT, classify(IOException("disk")))
        assertEquals(PrefsFailure.TRANSIENT, classify(OutOfMemoryError()))
        assertEquals(
            PrefsFailure.TRANSIENT,
            classify(GeneralSecurityException("wrap", ProviderException("busy")))
        )
    }

    @Test
    fun `explicit transient keystore failure wins over corrupt markers`() {
        val ks = KeyStoreException("android keystore", AEADBadTagException("tag"))
        // API 33+ の isTransientFailure() が true を返した想定 (probe を差し替える)
        val kind = PrefsFailure.classify(ks) { if (it === ks) true else null }
        assertEquals(PrefsFailure.TRANSIENT, kind)
    }

    @Test
    fun `other failures are UNKNOWN`() {
        assertEquals(PrefsFailure.UNKNOWN, classify(IllegalStateException("x")))
        assertEquals(PrefsFailure.UNKNOWN, classify(SecurityException("other")))
        assertEquals(PrefsFailure.UNKNOWN, classify(GeneralSecurityException("decryption failed")))
    }

    @Test
    fun `generation 0 keeps the legacy file name and key alias`() {
        // 既存ユーザーの通常経路は従来のファイル名・既定 alias のまま読めること
        assertEquals("sipbridge_enc", BridgeConfig.encPrefName(0))
        assertEquals("_androidx_security_master_key_", BridgeConfig.masterKeyAlias(0))
        // 作り直し後は別名 (元の鍵は消さない)
        assertEquals("sipbridge_enc2", BridgeConfig.encPrefName(1))
        assertEquals("sipbridge_master_key2", BridgeConfig.masterKeyAlias(1))
        assertEquals("sipbridge_enc3", BridgeConfig.encPrefName(2))
    }

    @Test
    fun `cause chain stops on cycles and describe lists class names`() {
        val a = RuntimeException("a")
        val b = IllegalStateException("b", a)
        a.initCause(b)   // a -> b -> a の循環
        assertEquals(2, PrefsFailure.causeChain(a).size)
        assertEquals(
            "java.io.IOException <- javax.crypto.AEADBadTagException",
            PrefsFailure.describe(IOException("secret message", AEADBadTagException("tag")))
        )
    }
}
