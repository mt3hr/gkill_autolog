package com.mt3hr.gkill_autolog

import android.content.Context
import android.os.Build
import android.util.Log

/**
 * 収集の設定。
 *
 * gkill への接続先はここには持たない。書き込むのは Termux の autolog で、
 * 接続先は /sdcard/gkill_autolog/config.env に書く（パスワードを含むため）。
 * このアプリは生ログを共有ストレージへ書き出すところまでを受け持つ。
 */
class Config(context: Context) {

    private val preferences =
        context.applicationContext.getSharedPreferences(PREFERENCES_NAME, Context.MODE_PRIVATE)

    /** この端末の名前。gkill の端末名と揃える。 */
    var device: String
        get() = preferences.getString(KEY_DEVICE, null) ?: defaultDeviceName()
        set(value) = preferences.edit().putString(KEY_DEVICE, value.trim()).apply()

    /** Chrome の履歴を root で読むかどうか。 */
    var readChromeHistory: Boolean
        get() = preferences.getBoolean(KEY_READ_CHROME_HISTORY, false)
        set(value) = preferences.edit().putBoolean(KEY_READ_CHROME_HISTORY, value).apply()

    /**
     * 毎時のスクリーンショットを撮るかどうか。
     * root の screencap を使うため、root が無い端末では何も起きない。
     */
    var captureScreenshots: Boolean
        get() = preferences.getBoolean(KEY_CAPTURE_SCREENSHOTS, false)
        set(value) = preferences.edit().putBoolean(KEY_CAPTURE_SCREENSHOTS, value).apply()

    /**
     * 端末名を config.env に合わせる。
     *
     * 端末名は autolog も使うので、二か所に持つと食い違う。
     * 実際、2台目を入れたときにアプリ側の既定値が前の端末のままで、
     * 別の端末で集めたログが前の端末のものとして記録されかけた。
     *
     * config.env は端末ごとに置くものなので、そちらを正とする。
     * 書かれていなければ、これまでどおりアプリの設定を使う。
     */
    fun syncDeviceFromSharedConfig() {
        val file = SharedStorage.configFile
        if (!file.canRead()) return

        val name = runCatching {
            file.readLines()
                .map { it.trim() }
                .filterNot { it.isEmpty() || it.startsWith("#") }
                .firstNotNullOfOrNull { line ->
                    line.split("=", limit = 2)
                        .takeIf { it.size == 2 && it[0].trim() == KEY_CONFIG_DEVICE }
                        ?.get(1)?.trim()
                }
        }.getOrNull()

        if (name.isNullOrBlank() || name == device) return

        Log.i(TAG, "端末名を config.env に合わせる: $device -> $name")
        device = name
    }

    companion object {
        private const val TAG = "AutologConfig"
        private const val PREFERENCES_NAME = "gkill_autolog"
        private const val KEY_DEVICE = "device"
        private const val KEY_READ_CHROME_HISTORY = "read_chrome_history"
        private const val KEY_CAPTURE_SCREENSHOTS = "capture_screenshots"

        /** config.env 側のキー。autolog の AUTOLOG_DEVICE と同じもの。 */
        private const val KEY_CONFIG_DEVICE = "AUTOLOG_DEVICE"

        /**
         * 端末名の既定値。
         *
         * gkill の端末名と揃えるのが本来なので config.env で決めるのが望ましい。
         * 決まっていないうちは機種名を使う。
         * 空白は <名前>_<端末>_<日付> の区切りと相性が悪いので落とす。
         */
        fun defaultDeviceName(): String =
            Build.MODEL.filterNot { it.isWhitespace() || it == '_' }
                .ifEmpty { "UnknownDevice" }
    }
}
