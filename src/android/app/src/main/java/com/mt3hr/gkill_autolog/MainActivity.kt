package com.mt3hr.gkill_autolog

import android.app.AppOpsManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.ActivityCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.mt3hr.gkill_autolog.collect.AudioCollector
import com.mt3hr.gkill_autolog.export.JsonlExporter
import com.mt3hr.gkill_autolog.store.GpsPointStore
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 設定と権限付与の画面。
 *
 * 必要な権限は Android の仕組み上、利用者が設定画面で許可するしかないものが多い。
 * ここではその設定画面を開くボタンと、現在の状態表示だけを用意する。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var config: Config
    private lateinit var statusView: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        config = Config(this)
        config.syncDeviceFromSharedConfig()
        statusView = findViewById(R.id.status)

        applyWindowInsets()

        val deviceInput = findViewById<EditText>(R.id.device)
        val appUsageCheckBox = findViewById<CheckBox>(R.id.collect_app_usage)
        val notificationCheckBox = findViewById<CheckBox>(R.id.collect_notification)
        val mediaPlayCheckBox = findViewById<CheckBox>(R.id.collect_media_play)
        val wifiCheckBox = findViewById<CheckBox>(R.id.collect_wifi)
        val bluetoothCheckBox = findViewById<CheckBox>(R.id.collect_bluetooth)
        val powerCheckBox = findViewById<CheckBox>(R.id.collect_power)
        val appUsageIntervalInput = findViewById<EditText>(R.id.app_usage_interval)
        val mediaPlayIntervalInput = findViewById<EditText>(R.id.media_play_interval)
        val chromeHistoryIntervalInput = findViewById<EditText>(R.id.chrome_history_interval)
        val exportIntervalInput = findViewById<EditText>(R.id.export_interval)
        val chromeCheckBox = findViewById<CheckBox>(R.id.read_chrome_history)
        val screenshotCheckBox = findViewById<CheckBox>(R.id.capture_screenshots)
        val screenshotIntervalInput = findViewById<EditText>(R.id.screenshot_interval)
        val captureOnUnlockCheckBox = findViewById<CheckBox>(R.id.capture_on_unlock)
        val locationCheckBox = findViewById<CheckBox>(R.id.record_location)
        val locationIntervalInput = findViewById<EditText>(R.id.location_interval)
        val locationAccuracyInput = findViewById<EditText>(R.id.location_accuracy)
        val highAccuracyCheckBox = findViewById<CheckBox>(R.id.high_accuracy_mode)
        val audioCheckBox = findViewById<CheckBox>(R.id.record_audio)
        val audioIntervalInput = findViewById<EditText>(R.id.audio_interval)
        val audioDurationInput = findViewById<EditText>(R.id.audio_duration)
        val audioScreenOffCheckBox = findViewById<CheckBox>(R.id.record_audio_while_screen_off)

        deviceInput.setText(config.device)
        appUsageCheckBox.isChecked = config.collectAppUsage
        notificationCheckBox.isChecked = config.collectNotifications
        mediaPlayCheckBox.isChecked = config.collectMediaPlay
        wifiCheckBox.isChecked = config.collectWifi
        bluetoothCheckBox.isChecked = config.collectBluetooth
        powerCheckBox.isChecked = config.collectPower
        appUsageIntervalInput.setText(config.appUsageIntervalSeconds.toString())
        mediaPlayIntervalInput.setText(config.mediaPlayIntervalSeconds.toString())
        chromeHistoryIntervalInput.setText(config.chromeHistoryIntervalSeconds.toString())
        exportIntervalInput.setText(config.exportIntervalMinutes.toString())
        chromeCheckBox.isChecked = config.readChromeHistory
        screenshotCheckBox.isChecked = config.captureScreenshots
        screenshotIntervalInput.setText(config.screenshotIntervalMinutes.toString())
        captureOnUnlockCheckBox.isChecked = config.captureOnUnlock
        locationCheckBox.isChecked = config.recordLocation
        locationIntervalInput.setText(config.locationIntervalSeconds.toString())
        locationAccuracyInput.setText(config.locationAccuracyMeters.toString())
        highAccuracyCheckBox.isChecked = config.highAccuracyMode
        audioCheckBox.isChecked = config.recordAudio
        audioIntervalInput.setText(config.audioIntervalMinutes.toString())
        audioDurationInput.setText(config.audioDurationMinutes.toString())
        audioScreenOffCheckBox.isChecked = config.recordAudioWhileScreenOff

        findViewById<Button>(R.id.save).setOnClickListener {
            config.device = deviceInput.text.toString()
            config.collectAppUsage = appUsageCheckBox.isChecked
            config.collectNotifications = notificationCheckBox.isChecked
            config.collectMediaPlay = mediaPlayCheckBox.isChecked
            config.collectWifi = wifiCheckBox.isChecked
            config.collectBluetooth = bluetoothCheckBox.isChecked
            config.collectPower = powerCheckBox.isChecked
            config.readChromeHistory = chromeCheckBox.isChecked
            config.captureScreenshots = screenshotCheckBox.isChecked
            config.captureOnUnlock = captureOnUnlockCheckBox.isChecked
            config.recordLocation = locationCheckBox.isChecked
            config.highAccuracyMode = highAccuracyCheckBox.isChecked
            config.recordAudio = audioCheckBox.isChecked
            config.recordAudioWhileScreenOff = audioScreenOffCheckBox.isChecked

            // 空欄や範囲外はそのまま使わず、扱える値へ丸めて画面へ返す。
            val interval = Config.clampLocationInterval(
                locationIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_LOCATION_INTERVAL_SECONDS
            )
            config.locationIntervalSeconds = interval
            locationIntervalInput.setText(interval.toString())

            val accuracy = Config.clampLocationAccuracy(
                locationAccuracyInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_LOCATION_ACCURACY_METERS
            )
            config.locationAccuracyMeters = accuracy
            locationAccuracyInput.setText(accuracy.toString())

            val screenshotInterval = Config.clampScreenshotInterval(
                screenshotIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_SCREENSHOT_INTERVAL_MINUTES
            )
            config.screenshotIntervalMinutes = screenshotInterval
            screenshotIntervalInput.setText(screenshotInterval.toString())

            // 録音は間隔を先に決める。長さは間隔より短くするので、
            // 丸めるのに確定した間隔が要る。
            val audioInterval = Config.clampAudioIntervalMinutes(
                audioIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_AUDIO_INTERVAL_MINUTES
            )
            config.audioIntervalMinutes = audioInterval
            audioIntervalInput.setText(audioInterval.toString())

            val audioDuration = Config.clampAudioDurationMinutes(
                audioDurationInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_AUDIO_DURATION_MINUTES,
                audioInterval,
            )
            config.audioDurationMinutes = audioDuration
            audioDurationInput.setText(audioDuration.toString())

            // 見に行く間隔。どれも同じ上下限（収集ループの周期〜1時間）で丸める。
            val appUsageInterval = Config.clampPollInterval(
                appUsageIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_POLL_INTERVAL_SECONDS
            )
            config.appUsageIntervalSeconds = appUsageInterval
            appUsageIntervalInput.setText(appUsageInterval.toString())

            val mediaPlayInterval = Config.clampPollInterval(
                mediaPlayIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_POLL_INTERVAL_SECONDS
            )
            config.mediaPlayIntervalSeconds = mediaPlayInterval
            mediaPlayIntervalInput.setText(mediaPlayInterval.toString())

            val chromeHistoryInterval = Config.clampPollInterval(
                chromeHistoryIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_CHROME_HISTORY_INTERVAL_SECONDS
            )
            config.chromeHistoryIntervalSeconds = chromeHistoryInterval
            chromeHistoryIntervalInput.setText(chromeHistoryInterval.toString())

            val exportInterval = Config.clampExportInterval(
                exportIntervalInput.text.toString().toIntOrNull()
                    ?: Config.DEFAULT_EXPORT_INTERVAL_MINUTES
            )
            config.exportIntervalMinutes = exportInterval
            exportIntervalInput.setText(exportInterval.toString())

            // 収集中なら、新しい設定で購読し直させる。
            // マイクつきの常駐を掴めるのはここ（画面が前に出ている）だけなので、
            // 音声をオンにしたときはこの経路が唯一の始まりどころになる。
            AutologService.reloadSettings(this)
            updateStatus()
        }

        findViewById<Button>(R.id.start).setOnClickListener {
            AutologService.start(this)
            updateStatus()
        }

        findViewById<Button>(R.id.stop).setOnClickListener {
            AutologService.stop(this)
            updateStatus()
        }

        findViewById<Button>(R.id.export_now).setOnClickListener { exportNow() }

        findViewById<Button>(R.id.permission_storage).setOnClickListener {
            requestManageExternalStorage()
        }
        findViewById<Button>(R.id.permission_usage).setOnClickListener {
            startSettings(Settings.ACTION_USAGE_ACCESS_SETTINGS)
        }
        findViewById<Button>(R.id.permission_notification).setOnClickListener {
            startSettings(Settings.ACTION_NOTIFICATION_LISTENER_SETTINGS)
        }
        findViewById<Button>(R.id.permission_accessibility).setOnClickListener {
            startSettings(Settings.ACTION_ACCESSIBILITY_SETTINGS)
        }
        findViewById<Button>(R.id.permission_location).setOnClickListener {
            requestLocationPermission()
        }
        findViewById<Button>(R.id.permission_background_location).setOnClickListener {
            requestBackgroundLocationPermission()
        }
        findViewById<Button>(R.id.permission_microphone).setOnClickListener {
            requestMicrophonePermission()
        }
        findViewById<Button>(R.id.permission_battery).setOnClickListener {
            requestIgnoreBatteryOptimizations()
        }
    }

    override fun onResume() {
        super.onResume()
        rearmMicrophone()
        updateStatus()
    }

    /**
     * マイクつきの常駐を掴み直す。
     *
     * マイクを使う常駐は、アプリがバックグラウンドにいる間は開始できない。
     * 端末の再起動から始まった常駐はマイクを持てないので、音声だけ記録されない。
     * この画面が見えているいまなら掴めるので、必要なら掴み直させる。
     *
     * これが再起動後に音声が戻る唯一の道になる。
     */
    private fun rearmMicrophone() {
        if (!config.recordAudio) return
        if (AutologService.isMicrophoneForegroundActive()) return
        if (!hasMicrophonePermission()) return
        AutologService.reloadSettings(this)
    }

    /**
     * 画面の上下がシステムバーに隠れないようにする。
     *
     * targetSdk 35 以降は端から端まで描画するのが既定になるため、
     * 余白を固定値にすると上部がステータスバーやアプリバーの下に潜る。
     * 実際の余白は端末とその時の表示状態で変わるので、
     * WindowInsets から受け取った値をそのまま padding にする。
     */
    private fun applyWindowInsets() {
        val scroll = findViewById<View>(R.id.root_scroll)
        ViewCompat.setOnApplyWindowInsetsListener(scroll) { view, windowInsets ->
            val insets = windowInsets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
            )
            view.setPadding(insets.left, insets.top, insets.right, insets.bottom)
            windowInsets
        }
        ViewCompat.requestApplyInsets(scroll)
    }

    /** 溜まっている生ログをその場で書き出す。 */
    private fun exportNow() {
        val button = findViewById<Button>(R.id.export_now)
        button.isEnabled = false
        statusView.text = getString(R.string.exporting)

        Thread {
            val result = runCatching { JsonlExporter(this).export() }
            runOnUiThread {
                button.isEnabled = true
                statusView.text = result.fold(
                    onSuccess = { count ->
                        if (count > 0) getString(R.string.export_done, count)
                        else getString(R.string.export_nothing)
                    },
                    onFailure = { getString(R.string.export_failed) },
                )
                // 少し見せてから通常の状態表示へ戻す。
                statusView.postDelayed({ updateStatus() }, STATUS_MESSAGE_MS)
            }
        }.start()
    }

    private fun updateStatus() {
        // ここで落ちると画面ごと死ぬ。表示のためだけの処理なので、
        // 失敗しても画面は開いたままにする。
        // 権限を変えて戻ってきた直後にも通る道なので、特に落とせない。
        statusView.text = runCatching { buildStatusText() }
            .getOrElse { "状態を取得できませんでした: ${it.message}" }
    }

    /**
     * 状況表示を組み立てる。
     *
     * この画面だけは文字列を strings.xml へ出さない。桁を揃えた等幅の
     * 診断表示で、書式と項目名が一体になっているためで、切り出すと
     * かえって崩れやすくなる。翻訳の予定も無い（他の言語のリソースは無い）。
     */
    private fun buildStatusText(): String {
        val pending = JsonlExporter(this).pendingCount()
        // 開きっぱなしにすると onResume のたびに接続が増える。
        val gpsPoints = GpsPointStore(this).use { it.count() }
        return buildString {
            appendLine("未書き出しのイベント: $pending 件")
            appendLine("記録した位置情報:     $gpsPoints 点")
            appendLine()
            appendLine("全ファイルアクセス:   ${mark(SharedStorage.canWrite())}")
            appendLine("使用状況へのアクセス: ${mark(hasUsageStatsPermission())}")
            appendLine("通知へのアクセス:     ${mark(hasNotificationAccess())}")
            appendLine("位置情報:             ${mark(hasLocationPermission())}")
            appendLine("位置情報(常に許可):   ${mark(hasBackgroundLocationPermission())}")
            appendLine("マイク:               ${mark(hasMicrophonePermission())}")
            appendLine("バッテリー最適化除外: ${mark(isIgnoringBatteryOptimizations())}")
            appendLine()
            appendLine("記録する種類: ${enabledCollectTypes()}")
            if (config.recordAudio) {
                appendLine("音声:         ${audioStatus()}")
            }
            appendLine()
            appendLine("書き出し先: ${SharedStorage.eventsDir}")
            appendLine("GPX:        ${SharedStorage.gpsLogDir}")
            appendLine("音声:       ${SharedStorage.audioDir}")
            append("取り込みは Termux の autolog が行います")

            // 落ちた記録があれば気づけるようにする。
            val crashLog = java.io.File(SharedStorage.root, AutologApp.CRASH_LOG_NAME)
            if (crashLog.exists()) {
                appendLine()
                appendLine()
                appendLine("落ちた記録があります: $crashLog")
                append(crashLog.readText().trim().takeLast(CRASH_EXCERPT_CHARS))
            }
        }
    }

    /**
     * 位置情報を「常に許可」にしてもらう。
     *
     * 画面が消えている間も GPX を記録するのに要る。
     * 通常の許可ダイアログでは出せず、アプリの設定画面から選ぶしかない。
     */
    private fun requestBackgroundLocationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            statusView.text = getString(R.string.background_location_not_needed)
            return
        }
        try {
            startActivity(
                Intent(
                    Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                    Uri.parse("package:$packageName")
                )
            )
            statusView.text = getString(R.string.background_location_hint)
        } catch (_: Exception) {
            statusView.text = getString(R.string.error_open_settings)
        }
    }

    private fun hasBackgroundLocationPermission(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) return true
        return checkSelfPermission(android.Manifest.permission.ACCESS_BACKGROUND_LOCATION) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED
    }

    private fun hasMicrophonePermission(): Boolean =
        checkSelfPermission(android.Manifest.permission.RECORD_AUDIO) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED

    /**
     * マイクを許可してもらう。
     *
     * 位置情報のボタンにまとめない。あのボタンは位置情報・Bluetooth・通知を
     * まとめて求めるもので、ラベルと出るダイアログを揃えてある。
     */
    private fun requestMicrophonePermission() {
        ActivityCompat.requestPermissions(
            this,
            arrayOf(android.Manifest.permission.RECORD_AUDIO),
            REQUEST_PERMISSIONS,
        )
    }

    /**
     * 全ファイルアクセスを求める。
     *
     * 生ログの受け渡し先 /sdcard/gkill_autolog は Termux の autolog も読む場所で、
     * アプリ専用領域では渡せない。targetSdk 30 以降、そこへ書くにはこの許可が要る。
     */
    private fun requestManageExternalStorage() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            statusView.text = getString(R.string.storage_permission_not_needed)
            return
        }
        try {
            startActivity(
                Intent(
                    Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION,
                    Uri.parse("package:$packageName")
                )
            )
        } catch (_: Exception) {
            startSettings(Settings.ACTION_MANAGE_ALL_FILES_ACCESS_PERMISSION)
        }
    }

    /** いま記録する設定になっている種類。端末の利用は常に記録するので出さない。 */
    private fun enabledCollectTypes(): String {
        val enabled = buildList {
            if (config.collectAppUsage) add("アプリ利用")
            if (config.collectNotifications) add("通知")
            if (config.collectMediaPlay) add("再生")
            if (config.readChromeHistory) add("Chrome履歴")
            if (config.collectWifi) add("Wi-Fi")
            if (config.collectBluetooth) add("Bluetooth")
            if (config.collectPower) add("充電")
            if (config.captureScreenshots) add("スクショ")
            if (config.recordLocation) add("位置情報")
            if (config.recordAudio) add("音声")
        }
        return if (enabled.isEmpty()) "端末の利用のみ" else enabled.joinToString("・")
    }

    /**
     * 音声が実際に録れているか。
     *
     * 再起動のあとはマイクを掴めず、設定はオンなのに録れていない状態になる。
     * それを黙って続けないよう、掴めているかと直近の結果をここに出す。
     */
    private fun audioStatus(): String {
        if (!AutologService.isMicrophoneForegroundActive()) {
            return "休止（アプリを開くと戻ります）"
        }
        val failure = AudioCollector.lastFailure
        if (failure.isNotEmpty()) return "録音中（直近の失敗: $failure）"

        val at = AudioCollector.lastRecordedAt
        if (at == 0L) return "録音中（まだ録れていません）"
        val stamp = SimpleDateFormat("MM-dd HH:mm", Locale.US).format(Date(at))
        return "録音中（最後に録れたのは $stamp）"
    }

    private fun mark(granted: Boolean) = if (granted) "許可" else "未許可"

    private fun startSettings(action: String) {
        try {
            startActivity(Intent(action))
        } catch (_: Exception) {
            statusView.text = getString(R.string.error_open_settings)
        }
    }

    /**
     * 使用状況へのアクセスは通常の権限ではなく AppOps で確認する。
     * 確認用の API はいずれも deprecated だが、これ以外に判定する手段が無い。
     */
    @Suppress("DEPRECATION")
    private fun hasUsageStatsPermission(): Boolean {
        val appOps = getSystemService(Context.APP_OPS_SERVICE) as? AppOpsManager ?: return false
        val mode = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            appOps.unsafeCheckOpNoThrow(
                AppOpsManager.OPSTR_GET_USAGE_STATS, android.os.Process.myUid(), packageName
            )
        } else {
            appOps.checkOpNoThrow(
                AppOpsManager.OPSTR_GET_USAGE_STATS, android.os.Process.myUid(), packageName
            )
        }
        return mode == AppOpsManager.MODE_ALLOWED
    }

    private fun hasNotificationAccess(): Boolean {
        val enabled = Settings.Secure.getString(contentResolver, "enabled_notification_listeners")
        return enabled?.contains(packageName) == true
    }

    // LocationCollector の判定 (FINE または COARSE) と揃える。
    // FINE だけを見ると、COARSE のみ許可した端末で「未許可」と表示され続けるのに
    // 収集は動く、というちぐはぐな状態になる。
    private fun hasLocationPermission(): Boolean =
        checkSelfPermission(android.Manifest.permission.ACCESS_FINE_LOCATION) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED ||
            checkSelfPermission(android.Manifest.permission.ACCESS_COARSE_LOCATION) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED

    private fun requestLocationPermission() {
        ActivityCompat.requestPermissions(
            this,
            arrayOf(
                android.Manifest.permission.ACCESS_FINE_LOCATION,
                android.Manifest.permission.BLUETOOTH_CONNECT,
                android.Manifest.permission.POST_NOTIFICATIONS,
            ),
            REQUEST_PERMISSIONS
        )
    }

    private fun isIgnoringBatteryOptimizations(): Boolean {
        val powerManager = getSystemService(Context.POWER_SERVICE) as? PowerManager ?: return false
        return powerManager.isIgnoringBatteryOptimizations(packageName)
    }

    @Suppress("BatteryLife")
    private fun requestIgnoreBatteryOptimizations() {
        try {
            startActivity(
                Intent(
                    Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                    Uri.parse("package:$packageName")
                )
            )
        } catch (_: Exception) {
            startSettings(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
        }
    }

    companion object {
        private const val REQUEST_PERMISSIONS = 1

        /** 書き出し結果を見せておく時間。 */
        private const val STATUS_MESSAGE_MS = 3000L

        /** 画面に出す、落ちた記録の末尾の文字数。 */
        private const val CRASH_EXCERPT_CHARS = 1200
    }
}
