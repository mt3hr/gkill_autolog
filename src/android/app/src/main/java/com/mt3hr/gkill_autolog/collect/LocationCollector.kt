package com.mt3hr.gkill_autolog.collect

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Build
import android.os.HandlerThread
import android.os.Looper
import android.os.SystemClock
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
 * Android 12 以降にある LocationManager.FUSED_PROVIDER は OS 側の融合測位なので、
 * Play 開発者サービスとは関係が無い。使えるなら使う。
 *
 * 記録間隔ごとの窓に対して、**その窓でいちばん精度の良い点だけ**を残す。
 * 以前は先に届いた点をそのまま採っていたため、屋内などで
 * 誤差 1km 級のセル測位が、数秒後に届く誤差 10m の GPS 測位に勝っていた。
 */
class LocationCollector(
    private val context: Context,
    private val store: GpsPointStore,
) {
    private val locationManager: LocationManager? =
        context.getSystemService(Context.LOCATION_SERVICE) as? LocationManager

    /** 購読できている provider。開始時に無効だったものを後から拾うために持つ。 */
    private val subscribed = mutableSetOf<String>()

    /**
     * 位置情報を受け取るスレッド。
     *
     * 受け取るたびに SQLite を読み書きするので、主スレッドでは受けない。
     * 高精度モードでは provider ごとに数秒おきに届くため、
     * 主スレッドで受けると設定画面の操作が引っかかる。
     */
    private var callbackThread: HandlerThread? = null

    /**
     * 購読したときの設定。
     *
     * 点が来るたびに設定を読み直すと、購読時に渡した値と食い違うことがある。
     * 設定を変えたときは [restart] で購読し直す。
     */
    private var intervalMs: Long = 0
    private var accuracyLimitMeters: Float = 0f
    private var highAccuracy = false

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
        val config = Config(context)
        if (!config.recordLocation) return
        if (!hasLocationPermission(context)) {
            Log.w(TAG, "位置情報の権限が無いため記録しない")
            return
        }

        intervalMs = config.locationIntervalSeconds * 1000L
        accuracyLimitMeters = config.locationAccuracyMeters.toFloat()
        highAccuracy = config.highAccuracyMode

        ensureSubscribed()
    }

    /**
     * まだ購読できていない provider を購読する。
     *
     * 開始したときに無効だった provider（機内モード中の GPS など）を
     * あとから拾えるようにするため、定期的に呼ぶ。
     * すでに購読できているものには触らない。
     */
    fun ensureSubscribed() {
        if (intervalMs == 0L) return
        val manager = locationManager ?: return
        if (!hasLocationPermission(context)) return

        // 測位の周期。高精度モードでは記録間隔より短くして候補を増やし、
        // その中からいちばん精度の良い点を選ぶ。
        val requestIntervalMs = if (highAccuracy) minOf(intervalMs, SAMPLE_INTERVAL_MS) else intervalMs
        val looper = callbackLooper()

        for (provider in providers()) {
            if (provider in subscribed) continue
            try {
                if (!manager.isProviderEnabled(provider)) continue
                // 距離のしきい値は 0。止まっている間も記録して、
                // 「そこに居た」ことが残るようにする。
                manager.requestLocationUpdates(provider, requestIntervalMs, 0f, listener, looper)
                subscribed.add(provider)
                Log.i(TAG, "$provider の購読を開始した (測位周期 ${requestIntervalMs / 1000} 秒)")
            } catch (e: SecurityException) {
                Log.w(TAG, "$provider を購読できなかった: ${e.message}")
            } catch (e: Exception) {
                Log.w(TAG, "$provider を購読できなかった: ${e.message}")
            }
        }
    }

    /** 位置情報を受け取るスレッドの Looper。無ければ立ち上げる。 */
    private fun callbackLooper(): Looper {
        val existing = callbackThread
        if (existing != null) return existing.looper

        val thread = HandlerThread("AutologLocation").apply { start() }
        callbackThread = thread
        return thread.looper
    }

    /**
     * 購読する provider。
     *
     * 屋内では GPS が入らずネットワーク側しか取れないことがあるので両方を購読し、
     * どれがいちばん精度が良かったかは点が届いてから判断する。
     */
    private fun providers(): List<String> = buildList {
        add(LocationManager.GPS_PROVIDER)
        add(LocationManager.NETWORK_PROVIDER)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            add(LocationManager.FUSED_PROVIDER)
        }
    }

    /** 記録を止める。 */
    fun stop() {
        if (subscribed.isNotEmpty()) {
            try {
                locationManager?.removeUpdates(listener)
            } catch (e: Exception) {
                Log.w(TAG, "購読を止められなかった: ${e.message}")
            }
            subscribed.clear()
        }

        // 購読を止めてから畳む。先に畳むと、残っていた通知の行き先が無くなる。
        callbackThread?.quitSafely()
        callbackThread = null
    }

    /** 設定が変わったときに購読し直す。 */
    fun restart() {
        stop()
        intervalMs = 0
        start()
    }

    private fun onLocation(location: Location) {
        val at = if (location.time > 0) location.time else System.currentTimeMillis()
        if (intervalMs == 0L) return

        // 購読を始めた直後は、以前に測った古い点がそのまま流れてくることがある。
        // いま居る場所として記録すると経路が飛ぶので捨てる。
        val ageMs = (SystemClock.elapsedRealtimeNanos() - location.elapsedRealtimeNanos) / 1_000_000
        if (ageMs > MAX_FIX_AGE_MS) {
            Log.d(TAG, "古い測位を捨てた (${ageMs / 1000} 秒前)")
            return
        }

        // 精度が取れなかった点は捨てない。順位はいちばん下に置き、
        // ほかに点が無ければ記録する。観測できた事実は残す。
        val accuracy = if (location.hasAccuracy()) location.accuracy else null
        if (accuracy != null && accuracy > accuracyLimitMeters) {
            Log.d(TAG, "粗い測位を捨てた (${location.provider} 誤差 ${accuracy.toInt()}m)")
            return
        }

        // 窓の中では、いちばん精度の良い点だけが残る。
        val windowStart = at / intervalMs * intervalMs
        store.putBest(
            localDate = GpxWriter.localDate(at),
            windowStart = windowStart,
            windowEnd = windowStart + intervalMs,
            at = at,
            latitude = location.latitude,
            longitude = location.longitude,
            altitude = if (location.hasAltitude()) location.altitude else null,
            accuracy = accuracy?.toDouble(),
        )
    }

    companion object {
        private const val TAG = "AutologLocation"

        /** 高精度モードのときの測位周期。記録間隔がこれより短ければ記録間隔を使う。 */
        private const val SAMPLE_INTERVAL_MS = 5_000L

        /** これより古い測位は使わない。 */
        private const val MAX_FIX_AGE_MS = 60_000L

        /**
         * 位置情報を扱える権限があるか。
         *
         * FINE と COARSE のどちらでも記録はできる（精度は FINE のほうが良い）。
         * 前景サービスの種別を決める [com.mt3hr.gkill_autolog.AutologService] と
         * 同じ判定にしておく。ここだけ FINE を要求していたころは、
         * COARSE だけ許可した端末でサービスは location 種別で立ち上がるのに
         * 収集側は黙って何も記録しない、という食い違いが起きていた。
         */
        fun hasLocationPermission(context: Context): Boolean =
            ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_FINE_LOCATION) ==
                PackageManager.PERMISSION_GRANTED ||
                ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_COARSE_LOCATION) ==
                PackageManager.PERMISSION_GRANTED
    }
}
