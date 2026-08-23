package com.mt3hr.gkill_autolog

// 編集前に読む: .claude/skills/autolog-android/SKILL.md（この領域の不変条件の正本）

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
        context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

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

    /**
     * 記録する種類。ここから下の6つは既定が true。
     *
     * 後から足した設定なので、既定を false にすると更新した時点で
     * それまで記録できていたものが黙って止まる。
     *
     * **端末の利用 (ロック解除・画面消灯・収集の開始と終了) には切り替えを置かない。**
     * 取り込み側がこれを使って利用セッションの区間を組み立てており、
     * 止めるとアプリ利用も再生も区間として閉じられなくなる
     * (normalize の window.go の継続中セッション、state.go の観測の切れ目)。
     */
    var collectAppUsage: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_APP_USAGE, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_APP_USAGE, value).apply()

    /** 通知を記録するかどうか。 */
    var collectNotifications: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_NOTIFICATION, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_NOTIFICATION, value).apply()

    /** 動画・音楽の再生を記録するかどうか。 */
    var collectMediaPlay: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_MEDIA_PLAY, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_MEDIA_PLAY, value).apply()

    /** Wi-Fi の接続を記録するかどうか。 */
    var collectWifi: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_WIFI, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_WIFI, value).apply()

    /** Bluetooth の接続を記録するかどうか。 */
    var collectBluetooth: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_BLUETOOTH, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_BLUETOOTH, value).apply()

    /** 充電を記録するかどうか。 */
    var collectPower: Boolean
        get() = preferences.getBoolean(KEY_COLLECT_POWER, true)
        set(value) = preferences.edit().putBoolean(KEY_COLLECT_POWER, value).apply()

    /**
     * アプリ利用を読み取る間隔（秒）。
     *
     * 読み取るのは前回の続きから今までなので、間隔を空けても取りこぼさない。
     * 記録される区間の内容も変わらない。変わるのは、区間が確定してから
     * 生ログに載るまでの遅れだけ。
     */
    var appUsageIntervalSeconds: Int
        get() = preferences.getInt(KEY_APP_USAGE_INTERVAL, DEFAULT_POLL_INTERVAL_SECONDS)
        set(value) = preferences.edit()
            .putInt(KEY_APP_USAGE_INTERVAL, clampPollInterval(value))
            .apply()

    /**
     * 動画・音楽の再生を見に行く間隔（秒）。
     *
     * **これは記録の粒度そのもの。** 再生時間は見に行った時点どうしの差で
     * 積み上げるので、間隔を空けるほど再生の始まりと終わりが粗くなる。
     * 電池のために空けるなら、そのぶん再生時間がずれることを承知で。
     */
    var mediaPlayIntervalSeconds: Int
        get() = preferences.getInt(KEY_MEDIA_PLAY_INTERVAL, DEFAULT_POLL_INTERVAL_SECONDS)
        set(value) = preferences.edit()
            .putInt(KEY_MEDIA_PLAY_INTERVAL, clampPollInterval(value))
            .apply()

    /** Chrome の履歴を root で読むかどうか。 */
    var readChromeHistory: Boolean
        get() = preferences.getBoolean(KEY_READ_CHROME_HISTORY, false)
        set(value) = preferences.edit().putBoolean(KEY_READ_CHROME_HISTORY, value).apply()

    /**
     * Chrome の履歴を読む間隔（秒）。
     *
     * su の起動と履歴DBのコピーを伴う重い処理なので、既定は長めにしてある。
     * 履歴は溜まってから読めるので、間隔を空けても取りこぼさない。
     */
    var chromeHistoryIntervalSeconds: Int
        get() = preferences.getInt(KEY_CHROME_HISTORY_INTERVAL, DEFAULT_CHROME_HISTORY_INTERVAL_SECONDS)
        set(value) = preferences.edit()
            .putInt(KEY_CHROME_HISTORY_INTERVAL, clampPollInterval(value))
            .apply()

    /**
     * 溜まった生ログを共有ストレージへ書き出す間隔（分）。
     *
     * 書き出せなかった分は端末に残って次回やり直されるので、
     * 間隔が長くても失われない。すぐ渡したいときは「今すぐ書き出し」を使う。
     */
    var exportIntervalMinutes: Int
        get() = preferences.getInt(KEY_EXPORT_INTERVAL, DEFAULT_EXPORT_INTERVAL_MINUTES)
        set(value) = preferences.edit()
            .putInt(KEY_EXPORT_INTERVAL, clampExportInterval(value))
            .apply()

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
     * 定期的に音声を録るかどうか。
     *
     * 録った音は共有ストレージへ置くだけで、生ログには入れない。
     * gkill へ運ぶのは同期スクリプトと gkill_server idf の役目
     * (スクリーンショットや GPX と同じ扱い)。
     */
    var recordAudio: Boolean
        get() = preferences.getBoolean(KEY_RECORD_AUDIO, false)
        set(value) = preferences.edit().putBoolean(KEY_RECORD_AUDIO, value).apply()

    /**
     * 録音の間隔（分）。
     *
     * 録音時刻はこの間隔で丸める。60 なら毎時00分、30 なら毎時00分と30分。
     * スクリーンショットの撮影間隔と同じ単位・同じ範囲にしてある。
     */
    var audioIntervalMinutes: Int
        get() = preferences.getInt(KEY_AUDIO_INTERVAL, DEFAULT_AUDIO_INTERVAL_MINUTES)
        set(value) = preferences.edit()
            .putInt(KEY_AUDIO_INTERVAL, clampAudioIntervalMinutes(value))
            .apply()

    /**
     * 1回の録音の長さ（分）。
     *
     * **間隔を超えられない。** 超えると、次の区切りが来ても前の録音が
     * 終わっていない。ただしその判定にはもう一方の設定が要るので、
     * ここでは単独の上下限だけを効かせる。両者の関係は
     * [clampAudioDurationMinutes] を使って保存するときに見る。
     */
    var audioDurationMinutes: Int
        get() = preferences.getInt(KEY_AUDIO_DURATION, DEFAULT_AUDIO_DURATION_MINUTES)
        set(value) = preferences.edit()
            .putInt(KEY_AUDIO_DURATION, value.coerceIn(MIN_AUDIO_DURATION_MINUTES, MAX_AUDIO_DURATION_MINUTES))
            .apply()

    /**
     * 画面が消えている間とロック中も録るかどうか。既定は録る。
     *
     * スクリーンショットは中身の無いロック画面を撮っても仕方がないので撮らないが、
     * 音は画面が消えていても記録すべき事実がある。
     *
     * なお、撮り逃した区切りをあとで録り直すことはしない。
     * あとで録った音は別の時刻の音で、区切りの時刻を名乗らせられない。
     */
    var recordAudioWhileScreenOff: Boolean
        get() = preferences.getBoolean(KEY_RECORD_AUDIO_WHILE_SCREEN_OFF, true)
        set(value) = preferences.edit()
            .putBoolean(KEY_RECORD_AUDIO_WHILE_SCREEN_OFF, value)
            .apply()

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
        // 定数名は各所の PREFS_NAME と揃える。ファイル名は保存済みデータとの互換のため変えない。
        private const val PREFS_NAME = "gkill_autolog"
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
        private const val KEY_COLLECT_APP_USAGE = "collect_app_usage"
        private const val KEY_COLLECT_NOTIFICATION = "collect_notification"
        private const val KEY_COLLECT_MEDIA_PLAY = "collect_media_play"
        private const val KEY_COLLECT_WIFI = "collect_wifi"
        private const val KEY_COLLECT_BLUETOOTH = "collect_bluetooth"
        private const val KEY_COLLECT_POWER = "collect_power"
        private const val KEY_RECORD_AUDIO = "record_audio"
        private const val KEY_AUDIO_INTERVAL = "audio_interval_minutes"
        private const val KEY_AUDIO_DURATION = "audio_duration_minutes"
        private const val KEY_RECORD_AUDIO_WHILE_SCREEN_OFF = "record_audio_while_screen_off"
        private const val KEY_APP_USAGE_INTERVAL = "app_usage_interval_seconds"
        private const val KEY_MEDIA_PLAY_INTERVAL = "media_play_interval_seconds"
        private const val KEY_CHROME_HISTORY_INTERVAL = "chrome_history_interval_seconds"
        private const val KEY_EXPORT_INTERVAL = "export_interval_minutes"

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

        /**
         * 録音間隔の既定値と上下限（分）。
         * スクリーンショットの撮影間隔と同じ単位・同じ範囲にしてある。
         */
        const val DEFAULT_AUDIO_INTERVAL_MINUTES = 60
        const val MIN_AUDIO_INTERVAL_MINUTES = 1
        const val MAX_AUDIO_INTERVAL_MINUTES = 1440

        /** 録音間隔を扱える範囲へ丸める。 */
        fun clampAudioIntervalMinutes(minutes: Int): Int =
            minutes.coerceIn(MIN_AUDIO_INTERVAL_MINUTES, MAX_AUDIO_INTERVAL_MINUTES)

        /** 1回の録音の長さの既定値と上下限（分）。 */
        const val DEFAULT_AUDIO_DURATION_MINUTES = 1
        const val MIN_AUDIO_DURATION_MINUTES = 1
        const val MAX_AUDIO_DURATION_MINUTES = 60

        /**
         * 録音の長さを扱える範囲へ丸める。
         *
         * **間隔より短くする。** 同じ長さにすると、次の区切りが来た時点で
         * まだ前の録音が終わっていない（開始の遅れと停止・書き出しのぶん）。
         * その区切りは飛ぶので、60分ごとに60分と入れると半分しか録れない。
         * 上下限だけでは弾けないので、間隔を渡してもらってここで抑える。
         */
        fun clampAudioDurationMinutes(minutes: Int, intervalMinutes: Int): Int {
            val interval = clampAudioIntervalMinutes(intervalMinutes)
            val limit = minOf(
                MAX_AUDIO_DURATION_MINUTES,
                // 間隔が1分のときだけは、これ以上短くできないので同じ長さを許す。
                maxOf(MIN_AUDIO_DURATION_MINUTES, interval - 1),
            )
            return minutes.coerceIn(MIN_AUDIO_DURATION_MINUTES, limit)
        }

        /**
         * 見に行く間隔の上下限（秒）。
         *
         * 下限は収集ループの周期（5秒）。これより短くしても回数は増えない。
         * アプリ利用・再生・Chrome 履歴で共通に使う。
         */
        const val MIN_POLL_INTERVAL_SECONDS = 5
        const val MAX_POLL_INTERVAL_SECONDS = 3600

        /** アプリ利用と再生を見に行く間隔の既定値（秒）。収集ループの周期と同じ。 */
        const val DEFAULT_POLL_INTERVAL_SECONDS = 5

        /** Chrome の履歴を読む間隔の既定値（秒）。重い処理なので長めにしてある。 */
        const val DEFAULT_CHROME_HISTORY_INTERVAL_SECONDS = 60

        /** 見に行く間隔を扱える範囲へ丸める。 */
        fun clampPollInterval(seconds: Int): Int =
            seconds.coerceIn(MIN_POLL_INTERVAL_SECONDS, MAX_POLL_INTERVAL_SECONDS)

        /** 書き出しの間隔の既定値と上下限（分）。 */
        const val DEFAULT_EXPORT_INTERVAL_MINUTES = 60
        const val MIN_EXPORT_INTERVAL_MINUTES = 1
        const val MAX_EXPORT_INTERVAL_MINUTES = 1440

        /** 書き出しの間隔を扱える範囲へ丸める。 */
        fun clampExportInterval(minutes: Int): Int =
            minutes.coerceIn(MIN_EXPORT_INTERVAL_MINUTES, MAX_EXPORT_INTERVAL_MINUTES)

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
