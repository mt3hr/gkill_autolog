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
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.model.SessionAction
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * 端末利用・Wi-Fi・Bluetooth・充電の状態変化をブロードキャストから記録する。
 *
 * 記録するのは状態が変わった瞬間だけ。区間へのまとめと短時間の再接続の結合は
 * X1 Yoga 側の normalize が行うので、ここでは判断しない。
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

    private val prefs
        get() = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            val now = System.currentTimeMillis()
            when (intent.action) {
                // 画面を点灯しただけでは記録しない。ロック解除されたときだけ利用開始とする（要件 §11.1）。
                Intent.ACTION_USER_PRESENT ->
                    store.put(sessionEvent(SessionAction.UNLOCK, now))

                Intent.ACTION_SCREEN_OFF ->
                    store.put(sessionEvent(SessionAction.SCREEN_OFF, now))

                WifiManager.NETWORK_STATE_CHANGED_ACTION ->
                    recordWifiState(now)

                BluetoothDevice.ACTION_ACL_CONNECTED ->
                    recordBluetooth(intent, connected = true, at = now)

                BluetoothDevice.ACTION_ACL_DISCONNECTED ->
                    recordBluetooth(intent, connected = false, at = now)

                Intent.ACTION_POWER_CONNECTED ->
                    store.put(powerEvent(charging = true, at = now))

                Intent.ACTION_POWER_DISCONNECTED ->
                    store.put(powerEvent(charging = false, at = now))
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

        // 起動時点の状態を記録しておく。
        val now = System.currentTimeMillis()
        recordWifiState(now)
        store.put(powerEvent(charging = isCharging(), at = now))
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

        store.put(
            Event.instant(
                EventType.BLUETOOTH, at,
                JSONObject().put("device_name", name).put("connected", connected)
            )
        )
    }

    private fun isCharging(): Boolean {
        val batteryManager = context.getSystemService(Context.BATTERY_SERVICE) as? BatteryManager
            ?: return false
        return batteryManager.isCharging
    }

    companion object {
        private const val PREFS_NAME = "system_event_collector"
        private const val KEY_LAST_SSID = "last_ssid"
    }
}
