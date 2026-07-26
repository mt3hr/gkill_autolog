package com.mt3hr.gkill_autolog.store

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import com.mt3hr.gkill_autolog.model.Event
import org.json.JSONObject

/**
 * 端末内の生ログ。追記専用。
 *
 * 送信できたイベントだけを消すので、X1 Yoga が止まっていても失われない。
 * 送信の成否が確定するまで保持し、確定するまで削除しない（要件 §17）。
 */
class EventStore(context: Context) : SQLiteOpenHelper(context, DATABASE_NAME, null, DATABASE_VERSION) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE pending_event (
              id          INTEGER PRIMARY KEY AUTOINCREMENT,
              event_id    TEXT NOT NULL UNIQUE,
              event_type  TEXT NOT NULL,
              start_time  INTEGER NOT NULL,
              end_time    INTEGER,
              captured_at INTEGER NOT NULL,
              payload     TEXT NOT NULL
            )
            """.trimIndent()
        )
        db.execSQL("CREATE INDEX idx_pending_start ON pending_event(start_time)")
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        // まだ移行の必要が無い。作り直す。
        db.execSQL("DROP TABLE IF EXISTS pending_event")
        onCreate(db)
    }

    /** イベントを追記する。同じ event_id は無視される。 */
    fun put(event: Event) {
        val values = ContentValues().apply {
            put("event_id", event.eventId)
            put("event_type", event.eventType)
            put("start_time", event.startTime)
            put("end_time", event.endTime)
            put("captured_at", event.capturedAt)
            put("payload", event.payload.toString())
        }
        writableDatabase.insertWithOnConflict(
            "pending_event", null, values, SQLiteDatabase.CONFLICT_IGNORE
        )
    }

    /** 未送信のイベントを古い順に取り出す。 */
    fun take(limit: Int): List<StoredEvent> {
        val result = mutableListOf<StoredEvent>()
        readableDatabase.query(
            "pending_event",
            arrayOf("id", "event_id", "event_type", "start_time", "end_time", "captured_at", "payload"),
            null, null, null, null, "start_time ASC, id ASC", limit.toString()
        ).use { cursor ->
            while (cursor.moveToNext()) {
                val endTimeIndex = cursor.getColumnIndexOrThrow("end_time")
                result.add(
                    StoredEvent(
                        rowId = cursor.getLong(cursor.getColumnIndexOrThrow("id")),
                        event = Event(
                            eventId = cursor.getString(cursor.getColumnIndexOrThrow("event_id")),
                            eventType = cursor.getString(cursor.getColumnIndexOrThrow("event_type")),
                            startTime = cursor.getLong(cursor.getColumnIndexOrThrow("start_time")),
                            endTime = if (cursor.isNull(endTimeIndex)) null else cursor.getLong(endTimeIndex),
                            capturedAt = cursor.getLong(cursor.getColumnIndexOrThrow("captured_at")),
                            payload = JSONObject(cursor.getString(cursor.getColumnIndexOrThrow("payload"))),
                        )
                    )
                )
            }
        }
        return result
    }

    /** 送信できたイベントを消す。送信に成功した分だけを渡すこと。 */
    fun delete(rowIds: List<Long>) {
        if (rowIds.isEmpty()) return
        val placeholders = rowIds.joinToString(",") { "?" }
        writableDatabase.delete(
            "pending_event",
            "id IN ($placeholders)",
            rowIds.map { it.toString() }.toTypedArray()
        )
    }

    /** 未送信の件数。 */
    fun pendingCount(): Int =
        readableDatabase.rawQuery("SELECT COUNT(*) FROM pending_event", null).use { cursor ->
            if (cursor.moveToFirst()) cursor.getInt(0) else 0
        }

    companion object {
        private const val DATABASE_NAME = "autolog_raw.db"
        private const val DATABASE_VERSION = 1

        /** 1回の送信で送る上限。 */
        const val UPLOAD_BATCH_SIZE = 500
    }
}

/** 保存済みイベント。rowId は送信成功後の削除に使う。 */
data class StoredEvent(val rowId: Long, val event: Event)
