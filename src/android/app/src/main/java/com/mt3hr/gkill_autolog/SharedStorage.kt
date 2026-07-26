package com.mt3hr.gkill_autolog

import android.os.Build
import android.os.Environment
import java.io.File

/**
 * 収集アプリと Termux の autolog が受け渡しに使う場所。
 *
 * 両者は別のアプリなので互いのアプリ専用領域は見えない。
 * 共有ストレージだけが接点になる。
 *
 * ここに SQLite は置かない。共有ストレージは FUSE で、
 * 複数プロセスから開くとファイルロックが期待どおりに効かない。
 * 生ログは追記専用の JSONL として渡し、autolog が自分の raw.db へ取り込む。
 */
object SharedStorage {

    /** /sdcard/gkill_autolog */
    val root: File
        get() = File(Environment.getExternalStorageDirectory(), "gkill_autolog")

    /** 生ログの JSONL を置く場所。autolog import が読んで消す。 */
    val eventsDir: File
        get() = File(root, "events")

    /** スクリーンショットの置き場。dvnf.sh が AutoScreenshot へ運ぶ。 */
    val screenshotsDir: File
        get() = File(root, "screenshots")

    /** autolog の設定ファイル。アプリは読み書きしない（gkill のパスワードを含むため）。 */
    val configFile: File
        get() = File(root, "config.env")

    /**
     * 共有ストレージへ書けるかどうか。
     *
     * targetSdk 30 以降、他アプリと共有するディレクトリへ自由に書くには
     * 全ファイルアクセス (MANAGE_EXTERNAL_STORAGE) の許可が要る。
     */
    fun canWrite(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            Environment.isExternalStorageManager()
        } else {
            Environment.getExternalStorageState() == Environment.MEDIA_MOUNTED
        }

    /** 書き出し先を用意する。許可が無ければ false。 */
    fun prepare(dir: File): Boolean {
        if (!canWrite()) return false
        return dir.exists() || dir.mkdirs()
    }
}
