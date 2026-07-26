package com.mt3hr.gkill_autolog.export

import android.content.Context
import android.util.Log
import com.mt3hr.gkill_autolog.SharedStorage
import com.mt3hr.gkill_autolog.store.GpsPoint
import com.mt3hr.gkill_autolog.store.GpsPointStore
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * 記録した位置情報を GPX として共有ストレージへ書き出す。
 *
 * 出力は /sdcard/gkill_autolog/gpslog/YYYYMMDD.gpx。
 * そこから先へ運ぶのは termux-tasker の dvnf.sh の役目で、
 * GPSLogs_<端末>_<日付>/ にまとめられ、gkill が gpslog rep として読む。
 *
 * **ファイル名は YYYYMMDD.gpx でなければならない。**
 * gkill は日付からこの名前を組み立てて探すため、違う名前だと見つけられない
 * (gps_log_repository_gpx_dir_impl.go の findGPXFileByDate)。
 * 日付はローカル日付で、中の時刻は UTC。
 */
class GpxWriter(context: Context) {

    private val store = GpsPointStore(context.applicationContext)

    /**
     * 点が入っている日をすべて書き出す。書き出した日数を返す。
     *
     * 当日だけでなく過去日も対象にする。日をまたいだ直後に前日分を
     * 書き残さないようにするため。
     */
    fun writeAll(): Int = synchronized(writeLock) {
        if (!SharedStorage.prepare(SharedStorage.gpsLogDir)) {
            Log.w(TAG, "共有ストレージへ書けないため書き出さない。全ファイルアクセスの許可が要る")
            return 0
        }

        var written = 0
        for (localDate in store.datesWithPoints()) {
            val points = store.pointsOf(localDate)
            if (points.isEmpty()) continue
            if (write(localDate, points)) written++
        }

        // 書き終えた古い日は落とす。数日は残して、書き損じても作り直せるようにする。
        store.deleteBefore(retentionBoundary())
        return written
    }

    /**
     * その日の全点から GPX を作り直し、置き換える。
     *
     * 追記はしない。GPX は閉じタグが要るので、書きかけのファイルを
     * dvnf がコピーすると解釈に失敗し、その rep の GPS ログ全体が読めなくなる。
     * いったん .tmp へ書いてから rename すれば、読む側からは常に
     * 出来上がったファイルしか見えない。
     */
    private fun write(localDate: String, points: List<GpsPoint>): Boolean {
        val destination = File(SharedStorage.gpsLogDir, "$localDate.gpx")
        val temporary = File(SharedStorage.gpsLogDir, "$localDate.gpx.tmp")

        return try {
            temporary.outputStream().use { out ->
                out.write(render(localDate, points).toByteArray(Charsets.UTF_8))
                // rename の前に確実に書き終える。
                out.fd.sync()
            }
            if (!temporary.renameTo(destination)) {
                Log.w(TAG, "書き出したファイルの名前を変えられなかった: ${temporary.name}")
                temporary.delete()
                return false
            }
            Log.i(TAG, "書き出した: ${destination.name} (${points.size} 点)")
            true
        } catch (e: Exception) {
            Log.w(TAG, "書き出せなかった。次回やり直す: ${e.message}")
            temporary.delete()
            false
        }
    }

    /**
     * GPX 1.0 を組み立てる。
     *
     * gkill が読むのは trkpt の lat / lon / time だけだが、
     * 既存の GPX（GPSLogger 出力）と同じ形にしておく。
     */
    private fun render(localDate: String, points: List<GpsPoint>): String {
        val builder = StringBuilder(points.size * 128)
        builder.append("""<?xml version="1.0" encoding="UTF-8" ?>""")
        builder.append("""<gpx version="1.0" creator="$CREATOR"""")
        builder.append(""" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"""")
        builder.append(""" xmlns="http://www.topografix.com/GPX/1/0"""")
        builder.append(""" xsi:schemaLocation="http://www.topografix.com/GPX/1/0""")
        builder.append(""" http://www.topografix.com/GPX/1/0/gpx.xsd">""")

        // ファイル全体の時刻。最初の点に合わせる。
        builder.append("<time>").append(formatUtc(points.first().at)).append("</time>")
        builder.append("<trk><name>").append(localDate).append("</name><trkseg>\n")

        for (point in points) {
            builder.append("""<trkpt lat="""").append(point.latitude)
            builder.append("""" lon="""").append(point.longitude).append("\">")
            if (point.altitude != null) {
                builder.append("<ele>").append(point.altitude).append("</ele>")
            }
            builder.append("<time>").append(formatUtc(point.at)).append("</time>")
            builder.append("</trkpt>\n")
        }

        builder.append("</trkseg></trk></gpx>")
        return builder.toString()
    }

    /** 点を残す下限の日付。これより古い日は消す。 */
    private fun retentionBoundary(): String {
        val boundary = System.currentTimeMillis() -
            GpsPointStore.RETENTION_DAYS * 24L * 60L * 60L * 1000L
        return localDate(boundary)
    }

    companion object {
        private const val TAG = "AutologGpx"
        private const val CREATOR = "gkill_autolog"

        /**
         * 書き出しの排他。
         *
         * 呼び出し元ごとに GpxWriter を作るので、インスタンスではなくここで持つ。
         */
        private val writeLock = Any()

        /**
         * GPX のファイル名になる日付 (YYYYMMDD)。
         *
         * ローカル日付。gkill は前後1日を余分に探すので、
         * 中の時刻が UTC でも整合する。
         */
        fun localDate(at: Long): String =
            SimpleDateFormat("yyyyMMdd", Locale.US).format(Date(at))

        /** GPX の時刻表記。UTC で書く。 */
        fun formatUtc(at: Long): String {
            val format = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US)
            format.timeZone = TimeZone.getTimeZone("UTC")
            return format.format(Date(at))
        }
    }
}
