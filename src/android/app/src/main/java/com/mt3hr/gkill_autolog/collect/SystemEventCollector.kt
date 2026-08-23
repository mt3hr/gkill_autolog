package com.mt3hr.gkill_autolog.collect

import android.bluetooth.BluetoothDevice
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.wifi.WifiManager
import android.os.BatteryManager
import android.os.Build
import androidx.core.content.ContextCompat
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.model.SessionAction
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * 端末利用・Wi-Fi・Bluetooth・充電の状態変化をブロードキャストから記録する。
 *
 * 記録するのは状態が変わった瞬間だけ。区間へのまとめと短時間の再接続の結合は
 * 取り込み時の normalize が行うので、ここでは判断しない。
 */
class SystemEventCollector(
    private val context: Context,
    private val store: EventStore,
) {
    private var registered = false

    /**
     * 直前に接続していた SSID。切断イベントを作るために覚えておく。
     *
     * プロセスが死んでも失わないよう永続化する。メモリだけで持つと、
     * 再起動のたびに切断イベントを作れなくなり、接続区間が
     * 閉じないまま残る。normalize 側はそれを「継続中」とみなすので、
     * 取り込みのカーソルがそこで止まってしまう。
     */
    private var lastSsid: String?
        get() = prefs.getString(KEY_LAST_SSID, null)
        set(value) {
            prefs.edit().apply {
                if (value == null) remove(KEY_LAST_SSID) else putString(KEY_LAST_SSID, value)
            }.apply()
        }

    /**
     * いま接続している Bluetooth 機器の名前。
     *
     * [lastSsid] と同じ理由で持つ。記録をやめるときに切断を書けないと、
     * 接続区間が閉じないまま何日でも育つ。
     */
    private var connectedBluetoothDevices: Set<String>
        get() = prefs.getStringSet(KEY_CONNECTED_BLUETOOTH, emptySet()) ?: emptySet()
        set(value) {
            // getStringSet が返す集合は書き換えてはいけないので、毎回作り直して渡す。
            prefs.edit().putStringSet(KEY_CONNECTED_BLUETOOTH, LinkedHashSet(value)).apply()
        }

    /**
     * 直前に記録した充電の状態。まだ記録していなければ null。
     *
     * これが無いと、設定を保存するたびに「充電中」を書くことになる。
     * 取り込み側は後から来た開始を採るので、そのぶん充電区間の始まりが
     * 遅い方へずれていく。
     */
    private var lastCharging: Boolean?
        get() = if (prefs.contains(KEY_LAST_CHARGING)) {
            prefs.getBoolean(KEY_LAST_CHARGING, false)
        } else {
            null
        }
        set(value) {
            prefs.edit().apply {
                if (value == null) remove(KEY_LAST_CHARGING) else putBoolean(KEY_LAST_CHARGING, value)
            }.apply()
        }

    private val prefs
        get() = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    /**
     * 記録する種類の判定。
     *
     * 受け取ってから枝ごとに見る。IntentFilter を組み替える形にすると、
     * 設定を変えるたびに登録し直す配線が要るうえ、収集ループが動いていない
     * ときの ACTION_RELOAD_SETTINGS の扱い（AutologService の collecting）にも
     * 触ることになる。受けてから捨てるほうが単純で、反映も即時になる。
     */
    private val config get() = Config(context)

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            val now = System.currentTimeMillis()

            // 端末の利用（ロック解除・画面消灯）には切り替えを置かない。
            // 取り込み側がこれを使って利用セッションの区間を組み立てており、
            // 止めるとアプリ利用も再生も区間として閉じられなくなる。
            when (intent.action) {
                // 画面を点灯しただけでは記録しない。ロック解除されたときだけ利用開始とする（要件 §11.1）。
                Intent.ACTION_USER_PRESENT ->
                    store.put(sessionEvent(SessionAction.UNLOCK, now))

                Intent.ACTION_SCREEN_OFF ->
                    store.put(sessionEvent(SessionAction.SCREEN_OFF, now))

                WifiManager.NETWORK_STATE_CHANGED_ACTION ->
                    if (config.collectWifi) recordWifiState(now)

                BluetoothDevice.ACTION_ACL_CONNECTED ->
                    if (config.collectBluetooth) recordBluetooth(intent, connected = true, at = now)

                BluetoothDevice.ACTION_ACL_DISCONNECTED ->
                    if (config.collectBluetooth) recordBluetooth(intent, connected = false, at = now)

                Intent.ACTION_POWER_CONNECTED ->
                    if (config.collectPower) recordPowerState(now, charging = true)

                Intent.ACTION_POWER_DISCONNECTED ->
                    if (config.collectPower) recordPowerState(now, charging = false)
            }
        }
    }

    fun start() {
        if (registered) return
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_USER_PRESENT)
            addAction(Intent.ACTION_SCREEN_OFF)
            addAction(WifiManager.NETWORK_STATE_CHANGED_ACTION)
            addAction(BluetoothDevice.ACTION_ACL_CONNECTED)
            addAction(BluetoothDevice.ACTION_ACL_DISCONNECTED)
            addAction(Intent.ACTION_POWER_CONNECTED)
            addAction(Intent.ACTION_POWER_DISCONNECTED)
        }
        ContextCompat.registerReceiver(context, receiver, filter, ContextCompat.RECEIVER_EXPORTED)
        registered = true

        // 起動時点の状態は [applySettings] が拾う。
        applySettings()
    }

    /**
     * 設定の変更を反映する。設定を保存したときと収集を始めるときに呼ぶ。
     *
     * **記録をやめるときは、開いている区間を閉じる。**
     * 控え（[lastSsid] / [connectedBluetoothDevices] / [lastCharging]）を
     * 残したまま記録を止めると、切断イベントが二度と出ない。取り込み側は
     * 切断を観測するまでその接続を継続中とみなすので、次に観測の切れ目
     * （収集の終了）が来るまで、ずっとつないでいたことになる。
     * 常駐が続いている限り切れ目は来ないので、何日でも育つ。
     *
     * **記録を始めるときは、いまの状態を拾う。** ブロードキャストは状態が
     * 変わったときにしか来ないので、これが無いと、繋いだままオンにしたものが
     * 次に切り替わるまで記録されない。[recordWifiState] と [recordPowerState] は
     * 控えと同じなら何も書かないので、保存のたびに呼んでも増えない。
     *
     * **Bluetooth だけは、始めるときに拾い直せない。** 接続中の機器の名前を
     * 知るにはプロファイルごとの非同期な問い合わせが要る。繋いだままオンに
     * した機器は、次に繋ぎ直したときから記録される。
     */
    fun applySettings() {
        val now = System.currentTimeMillis()

        if (config.collectWifi) {
            recordWifiState(now)
        } else {
            lastSsid?.let { previous ->
                store.put(wifiEvent(previous, connected = false, at = now))
                lastSsid = null
            }
        }

        if (config.collectPower) {
            recordPowerState(now)
        } else if (lastCharging == true) {
            store.put(powerEvent(charging = false, at = now))
            lastCharging = false
        }

        if (!config.collectBluetooth) {
            val connected = connectedBluetoothDevices
            if (connected.isNotEmpty()) {
                for (name in connected) {
                    store.put(bluetoothEvent(name, connected = false, at = now))
                }
                connectedBluetoothDevices = emptySet()
            }
        }
    }

    /**
     * 充電の状態を記録する。控えと同じなら何も書かない。
     *
     * 同じ状態を二度書くと、取り込み側が後から来た開始を採るため、
     * 充電区間の始まりが遅い方へずれる。
     */
    private fun recordPowerState(at: Long, charging: Boolean = isCharging()) {
        if (charging == lastCharging) return
        store.put(powerEvent(charging = charging, at = at))
        lastCharging = charging
    }

    fun stop() {
        if (!registered) return
        context.unregisterReceiver(receiver)
        registered = false
    }

    private fun sessionEvent(action: String, at: Long): Event =
        Event.instant(EventType.SESSION, at, JSONObject().put("action", action))

    private fun powerEvent(charging: Boolean, at: Long): Event =
        Event.instant(EventType.POWER, at, JSONObject().put("charging", charging))

    /**
     * Wi-Fi の接続状態を記録する。
     * SSID だけを保存し、BSSID は生ログにも残さない（要件 §9.1）。
     *
     * 切断時は SSID を取得できないため、直前に接続していた SSID を覚えておき、
     * その切断として記録する。これをしないと接続区間が閉じられない。
     */
    private fun recordWifiState(at: Long) {
        val ssid = currentSsid()
        if (ssid == lastSsid) return

        lastSsid?.let { previous ->
            store.put(wifiEvent(previous, connected = false, at = at))
        }
        ssid?.let { current ->
            store.put(wifiEvent(current, connected = true, at = at))
        }
        lastSsid = ssid
    }

    private fun wifiEvent(ssid: String, connected: Boolean, at: Long): Event =
        Event.instant(
            EventType.WIFI, at,
            JSONObject().put("ssid", ssid).put("connected", connected)
        )

    @Suppress("DEPRECATION")
    private fun currentSsid(): String? {
        if (ContextCompat.checkSelfPermission(context, android.Manifest.permission.ACCESS_FINE_LOCATION)
            != PackageManager.PERMISSION_GRANTED
        ) {
            // 位置情報が許可されていないと SSID は取得できない。推測はしない。
            return null
        }
        val wifiManager = context.applicationContext
            .getSystemService(Context.WIFI_SERVICE) as? WifiManager ?: return null
        val info = wifiManager.connectionInfo ?: return null
        val raw = info.ssid ?: return null
        val ssid = raw.trim('"')
        return if (ssid.isEmpty() || ssid == WifiManager.UNKNOWN_SSID) null else ssid
    }

    /** Bluetooth 機器名だけを記録する。アドレスは扱わない。 */
    private fun recordBluetooth(intent: Intent, connected: Boolean, at: Long) {
        if (ContextCompat.checkSelfPermission(context, android.Manifest.permission.BLUETOOTH_CONNECT)
            != PackageManager.PERMISSION_GRANTED && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S
        ) {
            return
        }
        val device: BluetoothDevice? =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                intent.getParcelableExtra(BluetoothDevice.EXTRA_DEVICE, BluetoothDevice::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra(BluetoothDevice.EXTRA_DEVICE)
            }
        val name = try {
            device?.name
        } catch (_: SecurityException) {
            null
        } ?: return

        // 記録をやめるときに切断を書けるよう、繋がっているものを覚えておく。
        connectedBluetoothDevices = if (connected) {
            connectedBluetoothDevices + name
        } else {
            connectedBluetoothDevices - name
        }

        store.put(bluetoothEvent(name, connected = connected, at = at))
    }

    private fun bluetoothEvent(name: String, connected: Boolean, at: Long): Event =
        Event.instant(
            EventType.BLUETOOTH, at,
            JSONObject().put("device_name", name).put("connected", connected)
        )

    private fun isCharging(): Boolean {
        val batteryManager = context.getSystemService(Context.BATTERY_SERVICE) as? BatteryManager
            ?: return false
        return batteryManager.isCharging
    }

    companion object {
        private const val PREFS_NAME = "system_event_collector"
        private const val KEY_LAST_SSID = "last_ssid"
        private const val KEY_CONNECTED_BLUETOOTH = "connected_bluetooth_devices"
        private const val KEY_LAST_CHARGING = "last_charging"
    }
}
