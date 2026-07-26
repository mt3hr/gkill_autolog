package com.mt3hr.gkill_autolog.collect

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Looper
import android.util.Log
import androidx.core.content.ContextCompat
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.export.GpxWriter
import com.mt3hr.gkill_autolog.store.GpsPointStore

/**
 * 位置情報を記録する。
 *
 * 溜めた点は [GpxWriter] が日別の GPX にする。生ログ (raw.db) には入れない。
 * GPX が最終形で、autolog import は関与しない。
 *
 * FusedLocationProviderClient ではなく LocationManager を使う。
 * Google Play 開発者サービスへの依存を増やさないため。
 */
class LocationCollector(
    private val context: Context,
    private val store: GpsPointStore,
) {
    private val locationManager: LocationManager? =
        context.getSystemService(Context.LOCATION_SERVICE) as? LocationManager

    private var listening = false

    /** 直近で採った点の時刻。間隔より短い間隔で来たものは捨てる。 */
    private var lastAcceptedAt: Long = 0

    /**
     * 購読したときの間隔。
     *
     * 点が来るたびに設定を読み直すと、購読時に渡した値と食い違うことがある。
     * 設定を変えたときは [restart] で購読し直す。
     */
    private var intervalMs: Long = 0

    /**
     * 位置情報の受け取り口。
     *
     * ラムダ（SAM 変換）にはしない。`onLocationChanged` 以外が既定実装になったのは
     * Android 11 からで、それより古い端末では抽象メソッドのまま呼ばれ
     * AbstractMethodError になる。minSdk は 26 なので明示的に実装する。
     */
    private val listener = object : LocationListener {
        override fun onLocationChanged(location: Location) = onLocation(location)

        @Deprecated("Android 11 以降は呼ばれないが、古い端末のために実装しておく")
        override fun onStatusChanged(provider: String?, status: Int, extras: android.os.Bundle?) = Unit

        override fun onProviderEnabled(provider: String) = Unit

        override fun onProviderDisabled(provider: String) = Unit
    }

    /**
     * 記録を始める。設定が無効か権限が無ければ何もしない。
     *
     * 画面が消えている間も記録する。そのためサービスは
     * foregroundServiceType に location を含んでいる必要がある。
     */
    fun start() {
        if (listening) return
        val config = Config(context)
        if (!config.recordLocation) return
        if (!hasPermission()) {
            Log.w(TAG, "位置情報の権限が無いため記録しない")
            return
        }

        val manager = locationManager ?: return
        intervalMs = config.locationIntervalSeconds * 1000L

        // GPS と ネットワークの両方を購読する。屋内では GPS が入らず、
        // ネットワーク側しか取れないことがある。
        for (provider in listOf(LocationManager.GPS_PROVIDER, LocationManager.NETWORK_PROVIDER)) {
            try {
                if (!manager.isProviderEnabled(provider)) {
                    Log.i(TAG, "$provider は無効")
                    continue
                }
                // 距離のしきい値は 0。止まっている間も記録して、
                // 「そこに居た」ことが残るようにする。
                manager.requestLocationUpdates(provider, intervalMs, 0f, listener, Looper.getMainLooper())
                listening = true
                Log.i(TAG, "$provider の購読を開始した (間隔 ${config.locationIntervalSeconds} 秒)")
            } catch (e: SecurityException) {
                Log.w(TAG, "$provider を購読できなかった: ${e.message}")
            } catch (e: Exception) {
                Log.w(TAG, "$provider を購読できなかった: ${e.message}")
            }
        }
    }

    /** 記録を止める。 */
    fun stop() {
        if (!listening) return
        try {
            locationManager?.removeUpdates(listener)
        } catch (e: Exception) {
            Log.w(TAG, "購読を止められなかった: ${e.message}")
        }
        listening = false
    }

    /** 設定が変わったときに購読し直す。 */
    fun restart() {
        stop()
        lastAcceptedAt = 0
        start()
    }

    private fun onLocation(location: Location) {
        val at = if (location.time > 0) location.time else System.currentTimeMillis()

        // provider をまたいで同じ時間帯の点が二重に来る。
        // 購読したときの間隔より短いものは捨てる。
        if (lastAcceptedAt != 0L && at - lastAcceptedAt < intervalMs) return
        lastAcceptedAt = at

        store.put(
            localDate = GpxWriter.localDate(at),
            at = at,
            latitude = location.latitude,
            longitude = location.longitude,
            altitude = if (location.hasAltitude()) location.altitude else null,
        )
    }

    private fun hasPermission(): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_FINE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    companion object {
        private const val TAG = "AutologLocation"
    }
}
