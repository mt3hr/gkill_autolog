package com.mt3hr.gkill_autolog.model

import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.UUID

/**
 * 共通生ログの1件。
 *
 * スキーマは gkill_autolog/schema/event.schema.json と対応している。
 * 元のタイトル・URL・通知内容は加工前の生値で保持する。
 */
data class Event(
    val eventId: String,
    val eventType: String,
    val startTime: Long,
    val endTime: Long?,
    val capturedAt: Long,
    val payload: JSONObject,
) {
    fun toJson(device: String): JSONObject = JSONObject().apply {
        put("schema_version", SCHEMA_VERSION)
        put("event_id", eventId)
        put("device", device)
        put("event_type", eventType)
        put("start_time", formatTime(startTime))
        if (endTime != null) {
            put("end_time", formatTime(endTime))
        }
        put("captured_at", formatTime(capturedAt))
        put("payload", payload)
    }

    companion object {
        const val SCHEMA_VERSION = 1

        // 生ログの時刻表記。オフセット付きで出す。
        private val timeFormat: ThreadLocal<SimpleDateFormat> = ThreadLocal.withInitial {
            SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.US)
        }

        fun formatTime(epochMillis: Long): String =
            timeFormat.get()!!.format(Date(epochMillis))

        /**
         * 瞬間イベントを作る。
         *
         * eventId を渡すと、同じ事実を作り直したときに同じIDになる。
         * 受け口は (device, event_id) で重複を弾くので、
         * 同じ事実を二度送っても二重登録にならない。
         */
        fun instant(
            eventType: String,
            at: Long,
            payload: JSONObject,
            eventId: String = UUID.randomUUID().toString(),
        ): Event = Event(
            eventId = eventId,
            eventType = eventType,
            startTime = at,
            endTime = null,
            capturedAt = System.currentTimeMillis(),
            payload = payload,
        )

        /** 期間イベントを作る。eventId の扱いは [instant] と同じ。 */
        fun interval(
            eventType: String,
            start: Long,
            end: Long,
            payload: JSONObject,
            eventId: String = UUID.randomUUID().toString(),
        ): Event = Event(
            eventId = eventId,
            eventType = eventType,
            startTime = start,
            endTime = end,
            capturedAt = System.currentTimeMillis(),
            payload = payload,
        )
    }
}

/** event_type の値。 */
object EventType {
    const val SESSION = "session"
    const val APP_USAGE = "app_usage"
    const val NOTIFICATION = "notification"
    const val BROWSER_VIEW = "browser_view"
    const val MEDIA_PLAY = "media_play"
    const val WIFI = "wifi"
    const val BLUETOOTH = "bluetooth"
    const val POWER = "power"
}

/** session イベントの action。 */
object SessionAction {
    const val UNLOCK = "unlock"
    const val SCREEN_OFF = "screen_off"
    const val LOCK = "lock"
    const val COLLECTOR_START = "collector_start"
    const val COLLECTOR_STOP = "collector_stop"
}
