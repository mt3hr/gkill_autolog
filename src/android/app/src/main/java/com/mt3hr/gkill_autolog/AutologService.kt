package com.mt3hr.gkill_autolog

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.util.Log
import com.mt3hr.gkill_autolog.collect.AppUsageCollector
import com.mt3hr.gkill_autolog.collect.ChromeHistoryCollector
import com.mt3hr.gkill_autolog.collect.MediaCollector
import com.mt3hr.gkill_autolog.collect.ScreenshotCollector
import com.mt3hr.gkill_autolog.collect.SystemEventCollector
import com.mt3hr.gkill_autolog.export.ExportWorker
import com.mt3hr.gkill_autolog.export.JsonlExporter
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.model.SessionAction
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 常駐して各収集を回すフォアグラウンドサービス。
 *
 * 通知の収集だけは NotificationListenerService が独立して動くため、ここには含まれない。
 */
class AutologService : Service() {

    private val store: EventStore by lazy { EventStore(applicationContext) }
    private val handler = Handler(Looper.getMainLooper())

    /**
     * 書き出しと撮影のスレッド。
     * 収集は主スレッドのタイマーで回しているので、ファイル入出力は必ず別スレッドで行う。
     */
    private val ioExecutor: ExecutorService = Executors.newSingleThreadExecutor()

    /** 直前に書き出した時刻。 */
    private var lastExportAt: Long = 0

    /** 書き出し中かどうか。前の書き出しが終わる前に次を積まないようにする。 */
    private val exporting = AtomicBoolean(false)

    private lateinit var systemEvents: SystemEventCollector
    private lateinit var appUsage: AppUsageCollector
    private lateinit var media: MediaCollector
    private lateinit var chromeHistory: ChromeHistoryCollector
    private lateinit var screenshots: ScreenshotCollector

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
        chromeHistory = ChromeHistoryCollector(store, applicationContext.cacheDir)
        screenshots = ScreenshotCollector(applicationContext, Config(applicationContext))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(NOTIFICATION_ID, buildNotification())

        store.put(
            Event.instant(
                EventType.SESSION, System.currentTimeMillis(),
                JSONObject().put("action", SessionAction.COLLECTOR_START)
            )
        )

        systemEvents.start()
        ExportWorker.schedule(applicationContext)

        handler.removeCallbacks(tick)
        handler.post(tick)

        // 強制終了されても再開させる。
        return START_STICKY
    }

    override fun onDestroy() {
        handler.removeCallbacks(tick)
        systemEvents.stop()
        media.flush()
        ioExecutor.shutdown()

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
        appUsage.collect(now)
        media.collect(now)

        if (Config(applicationContext).readChromeHistory) {
            // 前面表示を確認できた区間だけを渡す。
            chromeHistory.collect(appUsage.recentChromeForegroundRanges(), now)
        }

        // 撮影は root コマンドの実行を伴うので主スレッドでは行わない。
        ioExecutor.execute { screenshots.captureIfDue(now) }

        exportIfDue(now)
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
        if (now - lastExportAt < EXPORT_INTERVAL_MS) return
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

        /** 収集の間隔。MediaSession の再生時間もこの粒度で積み上げる。 */
        private const val TICK_INTERVAL_MS = 5_000L

        /**
         * 書き出しの間隔。
         *
         * 書き出せなかった分は端末に残って次回やり直されるので、間隔が長くても失われない。
         * すぐ書き出したいときはアプリの「今すぐ書き出し」を使う。
         */
        private const val EXPORT_INTERVAL_MS = 60 * 60 * 1000L

        fun start(context: Context) {
            val intent = Intent(context, AutologService::class.java)
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, AutologService::class.java))
            ExportWorker.cancel(context)
        }
    }
}
