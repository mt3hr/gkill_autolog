package com.mt3hr.gkill_autolog

import android.app.ActivityManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.util.Log
import androidx.core.content.ContextCompat
import com.mt3hr.gkill_autolog.collect.AppUsageCollector
import com.mt3hr.gkill_autolog.collect.AudioCollector
import com.mt3hr.gkill_autolog.collect.ChromeHistoryCollector
import com.mt3hr.gkill_autolog.collect.MediaCollector
import com.mt3hr.gkill_autolog.collect.ScreenshotCollector
import com.mt3hr.gkill_autolog.collect.LocationCollector
import com.mt3hr.gkill_autolog.collect.SystemEventCollector
import com.mt3hr.gkill_autolog.export.ExportWorker
import com.mt3hr.gkill_autolog.export.GpxWriter
import com.mt3hr.gkill_autolog.export.JsonlExporter
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.model.SessionAction
import com.mt3hr.gkill_autolog.store.EventStore
import com.mt3hr.gkill_autolog.store.GpsPointStore
import org.json.JSONObject
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 常駐して各収集を回すフォアグラウンドサービス。
 *
 * 通知の収集だけは NotificationListenerService が独立して動くため、ここには含まれない。
 */
class AutologService : Service() {

    private val store: EventStore by lazy { EventStore(applicationContext) }

    /**
     * 位置情報の置き場。サービスで1つだけ開いて使い回す。
     *
     * 呼ぶたびに開くと、SQLite のハンドルが閉じられないまま増えていく。
     */
    private val gpsStore: GpsPointStore by lazy { GpsPointStore(applicationContext) }

    private val handler = Handler(Looper.getMainLooper())

    /**
     * 書き出しと撮影のスレッド。
     * 共有ストレージへの書き出しや su 越しの読み取りは長引くことがあるので、
     * 主スレッドのタイマーで回している収集を巻き込まないよう、ここへ逃がす。
     * (イベントの SQLite への追記は端末内で軽いため主スレッドのまま行っている)
     */
    private val ioExecutor: ExecutorService = Executors.newSingleThreadExecutor()

    /** 直前に書き出した時刻。 */
    private var lastExportAt: Long = 0

    /** 直前に GPX を書いた時刻。 */
    private var lastGpxWriteAt: Long = 0

    /** 書き出し中かどうか。前の書き出しが終わる前に次を積まないようにする。 */
    private val exporting = AtomicBoolean(false)

    /** Chrome 履歴を読んでいる最中かどうか。su が遅くても積み上げないようにする。 */
    private val chromeCollecting = AtomicBoolean(false)

    /** 直前に Chrome 履歴を読んだ時刻。 */
    private var lastChromeCollectAt: Long = 0

    /** 直前にアプリ利用を読んだ時刻。 */
    private var lastAppUsageCollectAt: Long = 0

    /** 直前に再生を見に行った時刻。 */
    private var lastMediaCollectAt: Long = 0

    /**
     * 収集ループが動いているか。
     *
     * ACTION_RELOAD_SETTINGS の Intent は「動いているサービスの設定を入れ替える」
     * つもりのものだが、届く直前にサービスが死んでいると、この Intent で
     * サービスが**新規に**生成される。そのとき設定の入れ替えだけで戻ると、
     * tick も ExportWorker も始まらない「何も収集しない常駐」ができてしまう。
     * 動いていない状態で受けたら、普通の開始として扱うための目印。
     */
    private var collecting = false

    private lateinit var systemEvents: SystemEventCollector
    private lateinit var appUsage: AppUsageCollector
    private lateinit var media: MediaCollector
    private lateinit var chromeHistory: ChromeHistoryCollector
    private lateinit var screenshots: ScreenshotCollector
    private lateinit var location: LocationCollector
    private lateinit var audio: AudioCollector

    private val tick = object : Runnable {
        override fun run() {
            try {
                collectOnce()
            } catch (e: Exception) {
                // 1つの収集で落ちても常駐は続ける。
                Log.e(TAG, "収集中にエラーが起きた", e)
            }
            handler.postDelayed(this, TICK_INTERVAL_MS)
        }
    }

    override fun onCreate() {
        super.onCreate()
        // 収集を始める前に端末名を確定させる。
        // 書き出す生ログにこの名前が入るので、後から直すのが面倒になる。
        Config(applicationContext).syncDeviceFromSharedConfig()

        systemEvents = SystemEventCollector(applicationContext, store)
        appUsage = AppUsageCollector(applicationContext, store)
        media = MediaCollector(applicationContext, store)
        chromeHistory = ChromeHistoryCollector(applicationContext, store)
        screenshots = ScreenshotCollector(applicationContext, Config(applicationContext))
        location = LocationCollector(applicationContext, gpsStore)
        audio = AudioCollector(applicationContext, Config(applicationContext))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!startForegroundCompat()) {
            // 常駐に入れなければ収集はできない。落とさずに畳む。
            stopSelf()
            return START_NOT_STICKY
        }

        // 設定が変わっただけのときは、収集開始として記録し直さない。
        // ただし収集ループがまだ動いていない (この Intent でサービスが新規に
        // 生成された) 場合は、普通の開始として扱う。設定の入れ替えだけで戻ると、
        // 何も収集しないフォアグラウンドサービスが残り続ける。
        if (intent?.action == ACTION_RELOAD_SETTINGS && collecting) {
            location.restart()
            systemEvents.applySettings()
            return START_STICKY
        }

        store.put(
            Event.instant(
                EventType.SESSION, System.currentTimeMillis(),
                JSONObject().put("action", SessionAction.COLLECTOR_START)
            )
        )

        systemEvents.start()
        location.start()
        ExportWorker.schedule(applicationContext)

        handler.removeCallbacks(tick)
        handler.post(tick)
        collecting = true

        // 強制終了されても再開させる。
        return START_STICKY
    }

    override fun onDestroy() {
        handler.removeCallbacks(tick)
        collecting = false
        micForegroundActive = false
        systemEvents.stop()
        location.stop()
        // 録りかけがあれば確定させる。停止そのものは録音スレッドで行われるので、
        // ここでは待たない（主スレッドなので長く塞ぐと ANR になる）。
        audio.stop()
        media.flush()

        // 溜まっている点を書き残さない。
        // onDestroy は主スレッドなので、ファイル入出力は投げてから短く待つ。
        ioExecutor.execute { runCatching { GpxWriter(gpsStore).writeAll() } }
        ioExecutor.shutdown()
        runCatching { ioExecutor.awaitTermination(SHUTDOWN_WAIT_MS, TimeUnit.MILLISECONDS) }
        runCatching { gpsStore.close() }

        store.put(
            Event.instant(
                EventType.SESSION, System.currentTimeMillis(),
                JSONObject().put("action", SessionAction.COLLECTOR_STOP)
            )
        )
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun collectOnce() {
        val now = System.currentTimeMillis()
        val config = Config(applicationContext)

        // 見に行く間隔は設定で決まる。既定は収集ループの周期と同じ（5秒）で、
        // そのときは毎回通る。長くすると電池を使わなくなるが、
        // 再生は積み上げの粒度そのものなので、そのぶん時間が粗くなる。
        if (now - lastAppUsageCollectAt >= config.appUsageIntervalSeconds * SECOND_MS) {
            lastAppUsageCollectAt = now
            appUsage.collect(now)
        }
        if (now - lastMediaCollectAt >= config.mediaPlayIntervalSeconds * SECOND_MS) {
            lastMediaCollectAt = now
            media.collect(now)
        }

        // Chrome の履歴は root コマンドの実行と履歴DBのコピーを伴うので、
        // 撮影と同じく主スレッドでは行わない。su の許可待ちで固まると ANR になる。
        // 履歴は溜まってから読めるので、tick ごとには行わない。
        if (config.readChromeHistory &&
            now - lastChromeCollectAt >= config.chromeHistoryIntervalSeconds * SECOND_MS &&
            chromeCollecting.compareAndSet(false, true)
        ) {
            lastChromeCollectAt = now
            // 前面表示を確認できた区間だけを渡す。スナップショットを取ってから渡す。
            val ranges = appUsage.recentChromeForegroundRanges()
            ioExecutor.execute {
                try {
                    chromeHistory.collect(ranges, now)
                } finally {
                    chromeCollecting.set(false)
                }
            }
        }

        // 撮影は root コマンドの実行を伴うので主スレッドでは行わない。
        ioExecutor.execute { screenshots.captureIfDue(now) }

        // 録音は ioExecutor に載せない。数分かかるので、そこを塞ぐと
        // 書き出し・GPX・撮影・Chrome 履歴まで全部止まる。
        // AudioCollector は自分のスレッドを持っていて、ここでは区切りを見るだけ。
        //
        // マイクつきの常駐に入れていない間は録らない。掴めていないマイクから
        // 録っても無音になるだけで、録れなかった時間の音を作ることになる。
        // 途中でマイクを手放したときは、録りかけをそこで確定させる
        // （観測はそこで終わったので、開いたまま捨てない）。
        if (micForegroundActive) audio.recordIfDue(now) else audio.stop()

        writeGpxIfDue(now)
        exportIfDue(now)
    }

    /**
     * 溜まった位置情報を GPX として書き出す。
     *
     * 生ログの書き出しとは別にしてある。GPX は日付をまたぐと別ファイルになるので、
     * 1時間おきだと日付が変わった直後の点をしばらく書き残してしまう。
     */
    private fun writeGpxIfDue(now: Long) {
        if (!Config(applicationContext).recordLocation) return
        if (now - lastGpxWriteAt < GPX_WRITE_INTERVAL_MS) return

        lastGpxWriteAt = now

        // 開始したときに無効だった provider を、ここで拾い直す。
        // 機内モードを解除したあとなどに、購読が欠けたままにならないようにする。
        location.ensureSubscribed()

        ioExecutor.execute {
            try {
                GpxWriter(gpsStore).writeAll()
            } catch (e: Exception) {
                // 書けなかった分は点として残っているので、次回やり直す。
                Log.i(TAG, "GPX を書けなかった。次回やり直す: ${e.message}")
            }
        }
    }

    /**
     * 溜まった生ログを共有ストレージへ書き出す。
     *
     * 書き出した先を読んで gkill へ入れるのは Termux の autolog の役目。
     *
     * WorkManager の周期タスクは最初の実行まで最大15分待たされるうえ、
     * 端末の状態によっては後回しにされる。常駐しているのだから、
     * サービス自身が定期的に書き出すほうが確実で速い。
     * WorkManager 側はサービスが動いていないときの保険として残してある。
     */
    private fun exportIfDue(now: Long) {
        val intervalMs = Config(applicationContext).exportIntervalMinutes * MINUTE_MS
        if (now - lastExportAt < intervalMs) return
        if (!exporting.compareAndSet(false, true)) return

        lastExportAt = now
        ioExecutor.execute {
            try {
                val exported = JsonlExporter(applicationContext).export()
                if (exported > 0) {
                    Log.i(TAG, "$exported 件を書き出した")
                }
            } catch (e: Exception) {
                // 書き出せなかった分は端末に残る。次回やり直す。
                Log.i(TAG, "書き出せなかった。次回やり直す: ${e.message}")
            } finally {
                exporting.set(false)
            }
        }
    }

    /**
     * 常駐に入る。入れたかどうかを返す。
     *
     * **位置情報の種別は、実際に使えるときだけ宣言する。**
     * Android 14 以降、`location` を含むフォアグラウンドサービスは
     * 開始する時点で位置情報の権限を持っていないと SecurityException になる。
     * マニフェストに書いてあるだけで検査されるので、位置情報を使わない設定でも
     * 権限が無ければ収集そのものが始められなくなってしまう。
     *
     * 実際、これで「収集を開始」を押すとアプリが落ちた。
     *
     * **マイクの種別は、位置情報と同じようには扱えない。**
     * 位置情報には「常に許可」があるが、マイクにそれに当たる権限は無い。
     * そのため、アプリがバックグラウンドにいる間は権限があっても
     * マイクつきの常駐を*開始できない*（その場で例外になる）。
     * 端末の再起動から始めるときは名指しで禁止されてもいる。
     *
     * 位置情報と同じように単純に足すと、再起動のたびにここが失敗し、
     * 収集そのものが立ち上がらなくなる。マイクだけ失敗を飲み込んで、
     * 残りの種別で入り直す。音声は次にアプリが開かれたときに始まる。
     */
    private fun startForegroundCompat(): Boolean {
        val notification = buildNotification()

        // 種別の指定が要るのは Android 14 以降。それ以前はマニフェストの宣言で動く。
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            return try {
                startForeground(NOTIFICATION_ID, notification)
                micForegroundActive = wantsMicrophoneForegroundService()
                true
            } catch (e: Exception) {
                Log.e(TAG, "常駐に入れなかった", e)
                false
            }
        }

        var types = ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
        if (canUseLocationForegroundService()) {
            types = types or ServiceInfo.FOREGROUND_SERVICE_TYPE_LOCATION
        }

        if (wantsMicrophoneForegroundService()) {
            try {
                startForeground(
                    NOTIFICATION_ID, notification,
                    types or ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE,
                )
                micForegroundActive = true
                return true
            } catch (e: Exception) {
                Log.i(TAG, "マイクつきの常駐に入れなかった。音声以外は続ける: ${e.message}")
            }
        }

        // **ここで必ず startForeground を呼ぶ。**
        // startForegroundService で起こされた回は、常駐に入らずに戻ると
        // ForegroundServiceDidNotStartInTimeException で落ちる。
        // マイクを宣言し直せたら残す方が親切だが、そのために常駐へ入る手続きを
        // 飛ばすと、収集そのものが道連れになる。
        //
        // 剥がれるのは、設定を保存した直後にアプリが背景へ回ったときなど
        // ごく狭い場合だけ。そのとき録りかけは collectOnce の audio.stop() が
        // 確定させ、次にアプリを開いたときに録音が戻る。
        micForegroundActive = false

        return try {
            startForeground(NOTIFICATION_ID, notification, types)
            true
        } catch (e: Exception) {
            Log.e(TAG, "常駐に入れなかった", e)
            false
        }
    }

    /**
     * マイクつきの常駐にしたいか。
     *
     * 設定でオンにしていて、かつ権限があるときだけ。
     * どちらか欠けていると開始に失敗する（位置情報と同じ）。
     */
    private fun wantsMicrophoneForegroundService(): Boolean {
        if (!Config(applicationContext).recordAudio) return false
        return ContextCompat.checkSelfPermission(this, android.Manifest.permission.RECORD_AUDIO) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED
    }

    /**
     * 位置情報つきの常駐にできるか。
     *
     * 設定でオンにしていて、かつ権限があるときだけ。
     * どちらか欠けていると開始に失敗する。
     *
     * 権限の判定は [LocationCollector] と同じものを使う。
     * 別々に書いていたころは、ここが FINE と COARSE のどちらでも通すのに
     * 収集側は FINE だけを見ていたため、COARSE だけ許可した端末で
     * 位置情報つきの常駐に入るのに何も記録されない、という食い違いが起きていた。
     */
    private fun canUseLocationForegroundService(): Boolean {
        if (!Config(applicationContext).recordLocation) return false
        return LocationCollector.hasLocationPermission(this)
    }

    private fun buildNotification(): Notification {
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            manager.createNotificationChannel(
                NotificationChannel(
                    CHANNEL_ID,
                    getString(R.string.service_channel_name),
                    // 常駐通知が目立たないようにする。
                    NotificationManager.IMPORTANCE_MIN
                )
            )
        }

        val openApp = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )

        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.service_notification_title))
            .setContentText(getString(R.string.service_notification_text))
            .setSmallIcon(android.R.drawable.ic_menu_recent_history)
            .setContentIntent(openApp)
            .setOngoing(true)
            .build()
    }

    companion object {
        private const val TAG = "AutologService"
        private const val CHANNEL_ID = "gkill_autolog_service"
        private const val NOTIFICATION_ID = 1

        /**
         * マイクつきの常駐に入れているか。
         *
         * マイクはバックグラウンドから掴めないので、一度掴めたら録音していない間も
         * 持ち続ける。持てていない間は録らない（無音のファイルをでっち上げないため）。
         *
         * 設定画面が「音声が動いているか」を出すのと、
         * 掴み直しが要るかを判断するのに読む。設定画面もサービスも同じプロセスにある。
         */
        @Volatile
        private var micForegroundActive = false

        /** マイクつきの常駐に入れているか。設定画面から読む。 */
        fun isMicrophoneForegroundActive(): Boolean = micForegroundActive

        /**
         * 収集ループの周期。
         *
         * 何かを見に行く間隔の下限でもある。設定で決める各間隔は、
         * この周期のうえで「そろそろか」を見るだけなので、これより
         * 細かくはならない。周期そのものを設定にはしない。
         * 長くすると撮影や録音の区切りを跨いで見落とす。
         */
        private const val TICK_INTERVAL_MS = 5_000L

        private const val SECOND_MS = 1_000L
        private const val MINUTE_MS = 60 * SECOND_MS

        /**
         * GPX を書き出す間隔。
         *
         * 生ログより短くしてある。日付をまたぐとファイルが変わるので、
         * 間隔が長いと日付が変わった直後の点をしばらく書き残すことになる。
         */
        private const val GPX_WRITE_INTERVAL_MS = 60 * 1000L

        /** 停止時に、書き残しの GPX を待つ時間。長く待つと ANR になる。 */
        private const val SHUTDOWN_WAIT_MS = 2_000L

        /** 設定が変わったことをサービスへ伝える Intent の印。 */
        private const val ACTION_RELOAD_SETTINGS = "com.mt3hr.gkill_autolog.RELOAD_SETTINGS"

        /** 利用者の操作で収集を始める。以後、再起動しても再開する。 */
        fun start(context: Context) {
            Config(context).collectionEnabled = true
            val intent = Intent(context, AutologService::class.java)
            context.startForegroundService(intent)
        }

        /**
         * 収集が有効なときだけ始める。再起動・アプリ更新からの再開用。
         * 利用者が止めたものを勝手に再開しない。
         */
        fun startIfEnabled(context: Context) {
            if (!Config(context).collectionEnabled) return
            context.startForegroundService(Intent(context, AutologService::class.java))
        }

        /** 利用者の操作で収集を止める。通知の収集と再起動後の自動開始も止まる。 */
        fun stop(context: Context) {
            Config(context).collectionEnabled = false
            context.stopService(Intent(context, AutologService::class.java))
            ExportWorker.cancel(context)
        }

        /**
         * 設定の変更を反映させる。
         *
         * 位置情報の記録間隔は購読するときに渡すので、
         * 変えたら購読し直さないと効かない。
         * サービスが動いていなければ何も起きない。
         */
        fun reloadSettings(context: Context) {
            if (!isRunning(context)) return
            val intent = Intent(context, AutologService::class.java)
                .setAction(ACTION_RELOAD_SETTINGS)
            context.startForegroundService(intent)
        }

        private fun isRunning(context: Context): Boolean {
            val manager = context.getSystemService(Context.ACTIVITY_SERVICE) as? ActivityManager
                ?: return false
            @Suppress("DEPRECATION")
            return manager.getRunningServices(Int.MAX_VALUE)
                .any { it.service.className == AutologService::class.java.name }
        }
    }
}
