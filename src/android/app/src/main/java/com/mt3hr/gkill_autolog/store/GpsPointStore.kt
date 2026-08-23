package com.mt3hr.gkill_autolog.store

// 編集前に読む: .claude/skills/autolog-android/SKILL.md（この領域の不変条件の正本）

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
              accuracy   REAL,
              UNIQUE(local_date, at)
            )
            """.trimIndent()
        )
        db.execSQL("CREATE INDEX idx_gps_point_date ON gps_point(local_date, at)")
    }

    /**
     * 移行する。**テーブルを作り直してはいけない。**
     *
     * [com.mt3hr.gkill_autolog.export.GpxWriter] はその日の GPX を
     * DB の全点から毎回作り直す。ここで点を消すと、次の書き出しで
     * 書き出し済みの当日分の軌跡が短くなって消えてしまう。
     */
    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        if (oldVersion < 2) {
            // 移行前の点は精度が分からない。NULL のままにして「精度不明」として扱う。
            db.execSQL("ALTER TABLE gps_point ADD COLUMN accuracy REAL")
        }
    }

    /**
     * その窓でいちばん精度の良い1点だけを残す。
     *
     * localDate は GPX のファイル名になる日付 (YYYYMMDD)。
     * windowStart 以上 windowEnd 未満が1つの窓で、窓には常に1点しか入らない。
     *
     * 同じ窓に複数の provider から点が届く。以前は先に届いた点をそのまま採っていたので、
     * 誤差 1km 級のセル測位が、2秒後に届く誤差 10m の GPS 測位に勝っていた。
     * ここで精度を比べて、良いほうへ置き換える。
     *
     * メモリに溜めずその場で DB を更新するのは、途中でプロセスが落ちても
     * そこまでの点を失わないようにするため。あとから良い点が来れば上書きされる。
     *
     * accuracy が null なのは精度が取れなかった点。順位はいちばん下に置くが、
     * ほかに点が無ければ記録する。観測できた事実は残す。
     */
    fun putBest(
        localDate: String,
        windowStart: Long,
        windowEnd: Long,
        at: Long,
        latitude: Double,
        longitude: Double,
        altitude: Double?,
        accuracy: Double?,
    ) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            val existing = db.query(
                "gps_point",
                arrayOf("COALESCE(accuracy, $UNKNOWN_ACCURACY) AS rank"),
                "local_date = ? AND at >= ? AND at < ?",
                arrayOf(localDate, windowStart.toString(), windowEnd.toString()),
                null, null, "rank ASC", "1"
            ).use { cursor ->
                if (cursor.moveToFirst()) cursor.getDouble(0) else null
            }

            // すでに入っている点のほうが良ければ、そのままにする。
            if (existing != null && existing <= (accuracy ?: UNKNOWN_ACCURACY)) {
                db.setTransactionSuccessful()
                return
            }

            if (existing != null) {
                db.delete(
                    "gps_point",
                    "local_date = ? AND at >= ? AND at < ?",
                    arrayOf(localDate, windowStart.toString(), windowEnd.toString()),
                )
            }

            val values = ContentValues().apply {
                put("local_date", localDate)
                put("at", at)
                put("latitude", latitude)
                put("longitude", longitude)
                put("altitude", altitude)
                put("accuracy", accuracy)
            }
            db.insertWithOnConflict("gps_point", null, values, SQLiteDatabase.CONFLICT_REPLACE)
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    /** 指定した日の点を時刻順に返す。 */
    fun pointsOf(localDate: String): List<GpsPoint> {
        val result = mutableListOf<GpsPoint>()
        readableDatabase.query(
            "gps_point",
            arrayOf("at", "latitude", "longitude", "altitude", "accuracy"),
            "local_date = ?", arrayOf(localDate), null, null, "at ASC"
        ).use { cursor ->
            val altitudeIndex = cursor.getColumnIndexOrThrow("altitude")
            val accuracyIndex = cursor.getColumnIndexOrThrow("accuracy")
            while (cursor.moveToNext()) {
                result.add(
                    GpsPoint(
                        at = cursor.getLong(cursor.getColumnIndexOrThrow("at")),
                        latitude = cursor.getDouble(cursor.getColumnIndexOrThrow("latitude")),
                        longitude = cursor.getDouble(cursor.getColumnIndexOrThrow("longitude")),
                        altitude = if (cursor.isNull(altitudeIndex)) null else cursor.getDouble(altitudeIndex),
                        accuracy = if (cursor.isNull(accuracyIndex)) null else cursor.getDouble(accuracyIndex),
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
        private const val DATABASE_VERSION = 2

        /** 点を残しておく日数。これより古い日は消す。 */
        const val RETENTION_DAYS = 7

        /**
         * 精度が分からない点の順位。
         *
         * 精度で並べるときに、値のある点より必ず後ろへ来るだけの大きさにする。
         * 地球の円周より大きい値なので、実在する誤差と取り違えることはない。
         */
        private const val UNKNOWN_ACCURACY = 1.0e9
    }
}

/** 記録した1点。 */
data class GpsPoint(
    val at: Long,
    val latitude: Double,
    val longitude: Double,
    val altitude: Double?,
    /** 水平方向の誤差 (m)。取れなかった点は null。 */
    val accuracy: Double?,
)
