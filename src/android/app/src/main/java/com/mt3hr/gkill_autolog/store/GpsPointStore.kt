package com.mt3hr.gkill_autolog.store

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

/**
 * 記録した位置情報。日付ごとに GPX を作り直すために貯めておく。
 *
 * GPX は書くたびにその日の全点から作り直す（[com.mt3hr.gkill_autolog.export.GpxWriter]）。
 * 点をメモリだけで持つと、プロセスが落ちたときにその日のそれまでの分を失うので、
 * ここへ残しておく。
 *
 * 置き場はアプリ専用領域。共有ストレージは FUSE でロックが効かないため置かない。
 */
class GpsPointStore(context: Context) : SQLiteOpenHelper(context, DATABASE_NAME, null, DATABASE_VERSION) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE gps_point (
              id         INTEGER PRIMARY KEY AUTOINCREMENT,
              local_date TEXT NOT NULL,
              at         INTEGER NOT NULL,
              latitude   REAL NOT NULL,
              longitude  REAL NOT NULL,
              altitude   REAL,
              UNIQUE(local_date, at)
            )
            """.trimIndent()
        )
        db.execSQL("CREATE INDEX idx_gps_point_date ON gps_point(local_date, at)")
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        // まだ移行の必要が無い。作り直す。
        db.execSQL("DROP TABLE IF EXISTS gps_point")
        onCreate(db)
    }

    /**
     * 1点を記録する。
     *
     * localDate は GPX のファイル名になる日付 (YYYYMMDD)。
     * 同じ時刻の点は無視される。
     */
    fun put(localDate: String, at: Long, latitude: Double, longitude: Double, altitude: Double?) {
        val values = ContentValues().apply {
            put("local_date", localDate)
            put("at", at)
            put("latitude", latitude)
            put("longitude", longitude)
            put("altitude", altitude)
        }
        writableDatabase.insertWithOnConflict(
            "gps_point", null, values, SQLiteDatabase.CONFLICT_IGNORE
        )
    }

    /** 指定した日の点を時刻順に返す。 */
    fun pointsOf(localDate: String): List<GpsPoint> {
        val result = mutableListOf<GpsPoint>()
        readableDatabase.query(
            "gps_point",
            arrayOf("at", "latitude", "longitude", "altitude"),
            "local_date = ?", arrayOf(localDate), null, null, "at ASC"
        ).use { cursor ->
            val altitudeIndex = cursor.getColumnIndexOrThrow("altitude")
            while (cursor.moveToNext()) {
                result.add(
                    GpsPoint(
                        at = cursor.getLong(cursor.getColumnIndexOrThrow("at")),
                        latitude = cursor.getDouble(cursor.getColumnIndexOrThrow("latitude")),
                        longitude = cursor.getDouble(cursor.getColumnIndexOrThrow("longitude")),
                        altitude = if (cursor.isNull(altitudeIndex)) null else cursor.getDouble(altitudeIndex),
                    )
                )
            }
        }
        return result
    }

    /**
     * 点が入っている日を古い順に返す。
     *
     * 日をまたいだあと、前日分の GPX を書き残さないために使う。
     */
    fun datesWithPoints(): List<String> {
        val result = mutableListOf<String>()
        readableDatabase.rawQuery(
            "SELECT DISTINCT local_date FROM gps_point ORDER BY local_date ASC", null
        ).use { cursor ->
            while (cursor.moveToNext()) {
                result.add(cursor.getString(0))
            }
        }
        return result
    }

    /**
     * 古い日の点を消す。
     *
     * GPX を書いたあとも数日は残す。書き出しに失敗していた場合に作り直せるようにするため。
     */
    fun deleteBefore(localDate: String) {
        writableDatabase.delete("gps_point", "local_date < ?", arrayOf(localDate))
    }

    /** 貯まっている点の数。画面の表示に使う。 */
    fun count(): Int =
        readableDatabase.rawQuery("SELECT COUNT(*) FROM gps_point", null).use { cursor ->
            if (cursor.moveToFirst()) cursor.getInt(0) else 0
        }

    companion object {
        private const val DATABASE_NAME = "autolog_gps.db"
        private const val DATABASE_VERSION = 1

        /** 点を残しておく日数。これより古い日は消す。 */
        const val RETENTION_DAYS = 7
    }
}

/** 記録した1点。 */
data class GpsPoint(
    val at: Long,
    val latitude: Double,
    val longitude: Double,
    val altitude: Double?,
)
