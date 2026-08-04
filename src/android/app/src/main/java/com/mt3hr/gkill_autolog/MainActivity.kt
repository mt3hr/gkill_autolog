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
import com.mt3hr.gkill_autolog.export.JsonlExporter
import com.mt3hr.gkill_autolog.store.GpsPointStore

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
        val chromeCheckBox = findViewById<CheckBox>(R.id.read_chrome_history)
        val screenshotCheckBox = findViewById<CheckBox>(R.id.capture_screenshots)
        val screenshotIntervalInput = findViewById<EditText>(R.id.screenshot_interval)
        val captureOnUnlockCheckBox = findViewById<CheckBox>(R.id.capture_on_unlock)
        val locationCheckBox = findViewById<CheckBox>(R.id.record_location)
        val locationIntervalInput = findViewById<EditText>(R.id.location_interval)
        val locationAccuracyInput = findViewById<EditText>(R.id.location_accuracy)
        val highAccuracyCheckBox = findViewById<CheckBox>(R.id.high_accuracy_mode)

        deviceInput.setText(config.device)
        chromeCheckBox.isChecked = config.readChromeHistory
        screenshotCheckBox.isChecked = config.captureScreenshots
        screenshotIntervalInput.setText(config.screenshotIntervalMinutes.toString())
        captureOnUnlockCheckBox.isChecked = config.captureOnUnlock
        locationCheckBox.isChecked = config.recordLocation
        locationIntervalInput.setText(config.locationIntervalSeconds.toString())
        locationAccuracyInput.setText(config.locationAccuracyMeters.toString())
        highAccuracyCheckBox.isChecked = config.highAccuracyMode

        findViewById<Button>(R.id.save).setOnClickListener {
            config.device = deviceInput.text.toString()
            config.readChromeHistory = chromeCheckBox.isChecked
            config.captureScreenshots = screenshotCheckBox.isChecked
            config.captureOnUnlock = captureOnUnlockCheckBox.isChecked
            config.recordLocation = locationCheckBox.isChecked
            config.highAccuracyMode = highAccuracyCheckBox.isChecked

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

            // 収集中なら、新しい設定で購読し直させる。
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
        findViewById<Button>(R.id.permission_battery).setOnClickListener {
            requestIgnoreBatteryOptimizations()
        }
    }

    override fun onResume() {
        super.onResume()
        updateStatus()
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
            appendLine("バッテリー最適化除外: ${mark(isIgnoringBatteryOptimizations())}")
            appendLine()
            appendLine("書き出し先: ${SharedStorage.eventsDir}")
            appendLine("GPX:        ${SharedStorage.gpsLogDir}")
            append("取り込みは Termux の autolog.sh が行います")

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

        /** 送信結果を見せておく時間。 */
        private const val STATUS_MESSAGE_MS = 3000L

        /** 画面に出す、落ちた記録の末尾の文字数。 */
        private const val CRASH_EXCERPT_CHARS = 1200
    }
}
