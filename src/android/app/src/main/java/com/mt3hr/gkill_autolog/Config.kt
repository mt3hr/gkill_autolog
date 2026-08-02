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

    /**
     * この端末の名前。gkill の端末名と揃える。
     *
     * ファイル名（<端末>-<時刻>.jsonl や <端末>_<時刻>.webp）にそのまま入るので、
     * 使えない文字はここで落とす。`_` も区切りと衝突するため使えない
     * （autolog 側の ValidateDeviceName と同じ規則）。
     * 落とした結果が空になる入力は保存しない。
     */
    var device: String
        get() = preferences.getString(KEY_DEVICE, null)?.takeIf { it.isNotBlank() }
            ?: defaultDeviceName()
        set(value) {
            val sanitized = sanitizeDeviceName(value)
            if (sanitized.isEmpty()) {
                Log.w(TAG, "端末名に使える文字が無いため変更しない: $value")
                return
            }
            preferences.edit().putString(KEY_DEVICE, sanitized).apply()
        }

    /**
     * 収集が有効かどうか。
     *
     * 「収集を停止」で false になり、通知の収集（NotificationListener は
     * システムにバインドされたまま残る）も端末の再起動後の自動開始も止まる。
     * 「収集を開始」で true に戻る。既定は true（従来どおり再起動で再開する）。
     */
    var collectionEnabled: Boolean
        get() = preferences.getBoolean(KEY_COLLECTION_ENABLED, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECTION_ENABLED, value).apply()

    /** Chrome の履歴を root で読むかどうか。 */
    var readChromeHistory: Boolean
        get() = preferences.getBoolean(KEY_READ_CHROME_HISTORY, false)
        set(value) = preferences.edit().putBoolean(KEY_READ_CHROME_HISTORY, value).apply()

    /**
     * Chrome の履歴をどこまで読んだか（Chrome の時刻表現）。
     *
     * メモリだけに持つと、アプリの更新や強制終了のたびに履歴DBを
     * 先頭から読み直すことになる。処理カーソルなのでここへ永続化する。
     */
    var chromeHistoryLastVisitTime: Long
        get() = preferences.getLong(KEY_CHROME_HISTORY_LAST_VISIT, 0L)
        set(value) = preferences.edit().putLong(KEY_CHROME_HISTORY_LAST_VISIT, value).apply()

    /**
     * 定期的にスクリーンショットを撮るかどうか。
     * root の screencap を使うため、root が無い端末では何も起きない。
     */
    var captureScreenshots: Boolean
        get() = preferences.getBoolean(KEY_CAPTURE_SCREENSHOTS, false)
        set(value) = preferences.edit().putBoolean(KEY_CAPTURE_SCREENSHOTS, value).apply()

    /**
     * スクリーンショットを撮る間隔（分）。
     *
     * 撮影時刻はこの間隔で丸める。60 なら毎時00分、15 なら毎時00分・15分・30分・45分。
     * 極端な値にならないよう [MIN_SCREENSHOT_INTERVAL_MINUTES] 〜
     * [MAX_SCREENSHOT_INTERVAL_MINUTES] に丸める。
     */
    var screenshotIntervalMinutes: Int
        get() = preferences.getInt(KEY_SCREENSHOT_INTERVAL, DEFAULT_SCREENSHOT_INTERVAL_MINUTES)
        set(value) = preferences.edit()
            .putInt(KEY_SCREENSHOT_INTERVAL, clampScreenshotInterval(value))
            .apply()

    /**
     * 撮り逃したとき、次に画面を点けた時点で撮り直すかどうか。
     *
     * 区切りの時刻に画面が消えていると撮れない。スマホは大半の時間で
     * 画面が消えているので、これが無いとほとんど撮れない。
     *
     * 撮り直すときの記録時刻は、区切りの時刻ではなく**実際に撮れた時刻**にする。
     * 撮れなかった時間の画像をでっち上げないため。
     */
    var captureOnUnlock: Boolean
        get() = preferences.getBoolean(KEY_CAPTURE_ON_UNLOCK, true)
        set(value) = preferences.edit().putBoolean(KEY_CAPTURE_ON_UNLOCK, value).apply()

    /**
     * 位置情報を記録するかどうか。
     * 記録した点は日別の GPX になり、dvnf が GPSLogs へ運ぶ。
     */
    var recordLocation: Boolean
        get() = preferences.getBoolean(KEY_RECORD_LOCATION, false)
        set(value) = preferences.edit().putBoolean(KEY_RECORD_LOCATION, value).apply()

    /**
     * 位置情報を記録する間隔（秒）。
     *
     * 短くするほど経路は細かくなるが電池を使う。
     * 極端な値で消耗しないよう [MIN_LOCATION_INTERVAL_SECONDS] 〜
     * [MAX_LOCATION_INTERVAL_SECONDS] に丸める。
     */
    var locationIntervalSeconds: Int
        get() = preferences.getInt(KEY_LOCATION_INTERVAL, DEFAULT_LOCATION_INTERVAL_SECONDS)
        set(value) = preferences.edit()
            .putInt(KEY_LOCATION_INTERVAL, clampLocationInterval(value))
            .apply()

    /**
     * 記録してよい位置情報の誤差の上限（m）。
     *
     * これより粗い点は捨てる。屋内などで GPS が入らないとき、
     * セル測位が誤差 1km 級の点を返すことがあり、そのまま記録すると
     * 経路が大きく飛ぶ。取れなかった時間は点が無いままにして、
     * 分からないものを埋めない。
     */
    var locationAccuracyMeters: Int
        get() = preferences.getInt(KEY_LOCATION_ACCURACY, DEFAULT_LOCATION_ACCURACY_METERS)
        set(value) = preferences.edit()
            .putInt(KEY_LOCATION_ACCURACY, clampLocationAccuracy(value))
            .apply()

    /**
     * 高精度モード。
     *
     * 記録間隔より短い周期で測位して、その間隔の中でいちばん精度の良い点を残す。
     * 候補が増えるぶん精度は上がるが、測位の回数が増えるので電池を使う。
     * オフにすると測位の回数は記録間隔どおりに戻る。
     * どちらでも「いちばん精度の良い点を残す」動き自体は変わらない。
     */
    var highAccuracyMode: Boolean
        get() = preferences.getBoolean(KEY_HIGH_ACCURACY_MODE, true)
        set(value) = preferences.edit().putBoolean(KEY_HIGH_ACCURACY_MODE, value).apply()

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
        private const val KEY_COLLECTION_ENABLED = "collection_enabled"
        private const val KEY_READ_CHROME_HISTORY = "read_chrome_history"
        private const val KEY_CHROME_HISTORY_LAST_VISIT = "chrome_history_last_visit_time"
        private const val KEY_CAPTURE_SCREENSHOTS = "capture_screenshots"
        private const val KEY_RECORD_LOCATION = "record_location"
        private const val KEY_LOCATION_INTERVAL = "location_interval_seconds"
        private const val KEY_LOCATION_ACCURACY = "location_accuracy_meters"
        private const val KEY_HIGH_ACCURACY_MODE = "location_high_accuracy"
        private const val KEY_SCREENSHOT_INTERVAL = "screenshot_interval_minutes"
        private const val KEY_CAPTURE_ON_UNLOCK = "capture_on_unlock"

        /** 位置情報の記録間隔の既定値と上下限（秒）。 */
        const val DEFAULT_LOCATION_INTERVAL_SECONDS = 60
        const val MIN_LOCATION_INTERVAL_SECONDS = 10
        const val MAX_LOCATION_INTERVAL_SECONDS = 3600

        /** 記録間隔を扱える範囲へ丸める。 */
        fun clampLocationInterval(seconds: Int): Int =
            seconds.coerceIn(MIN_LOCATION_INTERVAL_SECONDS, MAX_LOCATION_INTERVAL_SECONDS)

        /** 許容する位置情報の誤差の既定値と上下限（m）。 */
        const val DEFAULT_LOCATION_ACCURACY_METERS = 100
        const val MIN_LOCATION_ACCURACY_METERS = 5
        const val MAX_LOCATION_ACCURACY_METERS = 1000

        /** 許容誤差を扱える範囲へ丸める。 */
        fun clampLocationAccuracy(meters: Int): Int =
            meters.coerceIn(MIN_LOCATION_ACCURACY_METERS, MAX_LOCATION_ACCURACY_METERS)

        /** スクリーンショットの撮影間隔の既定値と上下限（分）。 */
        const val DEFAULT_SCREENSHOT_INTERVAL_MINUTES = 60
        const val MIN_SCREENSHOT_INTERVAL_MINUTES = 1
        const val MAX_SCREENSHOT_INTERVAL_MINUTES = 1440

        /** 撮影間隔を扱える範囲へ丸める。 */
        fun clampScreenshotInterval(minutes: Int): Int =
            minutes.coerceIn(MIN_SCREENSHOT_INTERVAL_MINUTES, MAX_SCREENSHOT_INTERVAL_MINUTES)

        /** config.env 側のキー。autolog の AUTOLOG_DEVICE と同じもの。 */
        private const val KEY_CONFIG_DEVICE = "AUTOLOG_DEVICE"

        /**
         * 端末名の既定値。
         *
         * gkill の端末名と揃えるのが本来なので config.env で決めるのが望ましい。
         * 決まっていないうちは機種名を使う。固定の名前は置かない
         * （機種名すら取れない環境では空になり、書き出し側が設定を促す）。
         */
        fun defaultDeviceName(): String = sanitizeDeviceName(Build.MODEL)

        /**
         * 端末名として使えない文字を落とす。
         *
         * `_` と空白は <名前>_<端末>_<日付> の区切りと衝突する。
         * パス区切りなどはファイル名に使えず、書き出しが静かに失敗し続ける。
         */
        fun sanitizeDeviceName(name: String): String =
            name.filterNot {
                it.isWhitespace() || it.isISOControl() || it == '_' || it in "/\\:*?\"<>|"
            }
    }
}
