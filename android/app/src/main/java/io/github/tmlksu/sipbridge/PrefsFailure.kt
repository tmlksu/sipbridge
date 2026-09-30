package io.github.tmlksu.sipbridge

/**
 * EncryptedSharedPreferences を開けなかった原因の分類 (#42)。Android 依存なし (JVM 単体テスト可)。
 *
 * - [CORRUPT]: 保存データか鍵が壊れている / 失われている。再試行しても直らないため、
 *   壊れたファイルを退避して新しい鍵・新しいファイルで作り直す。
 * - [TRANSIENT]: Keystore の一時的な失敗など。データは消さずに再試行する。
 * - [UNKNOWN]: どちらとも言えない。呼び出し側は [TRANSIENT] と同じに扱う (消さない側に倒す)。
 */
enum class PrefsFailure { TRANSIENT, CORRUPT, UNKNOWN;

    companion object {
        /** 壊れている (作り直すしかない) と判断する例外のクラス名。 */
        private val CORRUPT_CLASSES = setOf(
            "javax.crypto.AEADBadTagException",
            "com.google.crypto.tink.shaded.protobuf.InvalidProtocolBufferException",
            "java.io.CharConversionException",
            "android.security.keystore.KeyPermanentlyInvalidatedException",
            "java.security.UnrecoverableKeyException",
        )

        /** 一時的な失敗と判断する例外のクラス名 (サブクラスも含む)。 */
        private val TRANSIENT_CLASSES = setOf(
            "java.security.KeyStoreException",
            "java.security.ProviderException",
            "java.io.IOException",
        )

        /** API 33+ の `android.security.KeyStoreException`。isTransientFailure() で判定する。 */
        private const val ANDROID_KEYSTORE_EXCEPTION = "android.security.KeyStoreException"

        /**
         * [t] の cause chain をたどって分類する。
         * 1. API 33+ の `android.security.KeyStoreException#isTransientFailure()` が true → TRANSIENT
         * 2. chain のどこかに壊れを示す例外 → CORRUPT
         * 3. chain のどこかに一時的な失敗を示す例外 / [Error] → TRANSIENT
         * 4. それ以外 → UNKNOWN
         *
         * [transientProbe] は `isTransientFailure()` の呼び出し (テストで差し替える)。
         */
        fun classify(
            t: Throwable,
            transientProbe: (Throwable) -> Boolean? = ::probeAndroidKeyStoreTransient,
        ): PrefsFailure {
            val chain = causeChain(t)
            if (chain.any { transientProbe(it) == true }) return TRANSIENT
            if (chain.any { isCorrupt(it) }) return CORRUPT
            if (chain.any { e -> e is Error || TRANSIENT_CLASSES.any { isInstanceOfName(e, it) } }) {
                return TRANSIENT
            }
            return UNKNOWN
        }

        /** ログ用: cause chain のクラス名 (`A <- B <- C`)。メッセージは出さない。 */
        fun describe(t: Throwable): String =
            causeChain(t).joinToString(" <- ") { it.javaClass.name }

        /** 自身から根本原因までの例外 (循環は打ち切る。最大 16 段)。 */
        fun causeChain(t: Throwable): List<Throwable> {
            val out = ArrayList<Throwable>()
            var cur: Throwable? = t
            while (cur != null && out.none { it === cur } && out.size < 16) {
                out += cur
                cur = cur.cause
            }
            return out
        }

        private fun isCorrupt(e: Throwable): Boolean {
            if (CORRUPT_CLASSES.any { isInstanceOfName(e, it) }) return true
            // EncryptedSharedPreferences は値を復号できないと SecurityException("Could not decrypt ...") を投げる。
            return e is SecurityException && e.message?.contains("Could not decrypt") == true
        }

        /** [e] のクラスか親クラスの名前が [name] か (Android 専用クラスを JVM でも参照せずに判定する)。 */
        private fun isInstanceOfName(e: Throwable, name: String): Boolean {
            var c: Class<*>? = e.javaClass
            while (c != null) {
                if (c.name == name) return true
                c = c.superclass
            }
            return false
        }

        /** `android.security.KeyStoreException#isTransientFailure()` (API 33+)。該当しなければ null。 */
        private fun probeAndroidKeyStoreTransient(e: Throwable): Boolean? {
            if (!isInstanceOfName(e, ANDROID_KEYSTORE_EXCEPTION)) return null
            return runCatching {
                e.javaClass.getMethod("isTransientFailure").invoke(e) as? Boolean
            }.getOrNull()
        }
    }
}
