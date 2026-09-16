package net.peyan.sipbridge

import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.fragment.app.Fragment
import com.google.android.material.navigation.NavigationBarView
import com.google.android.material.navigationrail.NavigationRailView

/**
 * UI-DESIGN §1: ナビ + 4 Fragment (キーパッド/履歴/連絡先/設定)。起動時はキーパッド。
 * 縦画面はボトムナビ、横画面 (Echo Show 5) は左サイドレール
 * (`layout-land/activity_main.xml`。ボトムナビだとキーパッドの縦が足りない)。
 * どちらも `NavigationBarView` なので扱いは共通。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var bottomNav: NavigationBarView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        bottomNav = findViewById(R.id.bottomNav)
        applyWindowInsets()
        bottomNav.setOnItemSelectedListener { item ->
            showTab(item.itemId)
            true
        }
        if (savedInstanceState == null) {
            showTab(R.id.nav_keypad)
        }
    }

    /**
     * targetSdk 35 (Android 15) では edge-to-edge が強制され、システムバーの下に
     * 描画される。インセットをここで一括して配る:
     * 縦 (ボトムナビ): 上・左右 → コンテンツの padding、下 → ナビの padding。
     * 横 (サイドレール): 上・右・下 → コンテンツ、左・上・下 → レール。
     * CONSUMED を返して子への再配布を止め、ナビ自身の自動インセット適用
     * (56dp 固定高のままアイコンとラベルが潰れる原因) を無効にする。
     */
    private fun applyWindowInsets() {
        val root = findViewById<View>(R.id.rootMain)
        val container = findViewById<View>(R.id.fragmentContainer)
        ViewCompat.setOnApplyWindowInsetsListener(root) { _, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
            )
            if (bottomNav is NavigationRailView) {
                container.updatePadding(top = bars.top, right = bars.right, bottom = bars.bottom)
                bottomNav.updatePadding(left = bars.left, top = bars.top, bottom = bars.bottom)
            } else {
                container.updatePadding(left = bars.left, top = bars.top, right = bars.right)
                bottomNav.updatePadding(left = bars.left, right = bars.right, bottom = bars.bottom)
            }
            WindowInsetsCompat.CONSUMED
        }
        ViewCompat.requestApplyInsets(root)
    }

    /** キーパッドのモードチップなど、他タブから設定タブへ飛ぶための入口。 */
    fun selectTab(itemId: Int) {
        bottomNav.selectedItemId = itemId
    }

    private fun showTab(itemId: Int) {
        val frag: Fragment = when (itemId) {
            R.id.nav_history -> supportFragmentManager.findFragmentByTag(TAG_HISTORY)
                ?: HistoryFragment()
            R.id.nav_contacts -> supportFragmentManager.findFragmentByTag(TAG_CONTACTS)
                ?: ContactsFragment()
            R.id.nav_settings -> supportFragmentManager.findFragmentByTag(TAG_SETTINGS)
                ?: SettingsFragment()
            else -> supportFragmentManager.findFragmentByTag(TAG_KEYPAD)
                ?: KeypadFragment()
        }
        val tag = when (itemId) {
            R.id.nav_history -> TAG_HISTORY
            R.id.nav_contacts -> TAG_CONTACTS
            R.id.nav_settings -> TAG_SETTINGS
            else -> TAG_KEYPAD
        }
        supportFragmentManager.beginTransaction()
            .replace(R.id.fragmentContainer, frag, tag)
            .commit()
    }

    companion object {
        private const val TAG_KEYPAD = "keypad"
        private const val TAG_HISTORY = "history"
        private const val TAG_CONTACTS = "contacts"
        private const val TAG_SETTINGS = "settings"
    }
}
