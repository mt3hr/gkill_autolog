package com.mt3hr.gkill_autolog.collect

import android.util.Log
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject
import java.io.File

/**
 * root 権限で Chrome の履歴DBを読み、閲覧を記録する。
 *
 * Chrome の履歴DBはアプリ専用領域にあるため root でしか読めない。
 * DB を直接開くとロックや WAL の問題があるので、いったんコピーしてから読む。
 *
 * 履歴には「いつ開いたか」しか無く、前面に表示していたかは分からない。
 * そのため [AppUsageCollector] が記録した Chrome の利用区間と突き合わせ、
 * **Chrome を実際に使っていた時間帯の訪問だけ**を記録する（要件 §12）。
 * 突き合わせは X1 Yoga 側の normalize では行えないため、ここで browser_focused を立てる。
 *
 * DB の構造が変わって読めなくなった場合は、何も記録せずに諦める。
 * 生ログは残っているので、修正後に取り込み直せる（要件 §12）。
 */
class ChromeHistoryCollector(
    private val store: EventStore,
    private val cacheDir: File,
) {
    /** 前回どこまで読んだか。Chrome の時刻表現で持つ。 */
    private var lastVisitTime: Long = 0

    /**
     * Chrome を前面で使っていた区間を渡すと、その時間帯の訪問だけを記録する。
     * 区間が空なら何もしない。
     */
    fun collect(chromeForegroundRanges: List<LongRange>, now: Long = System.currentTimeMillis()) {
        if (chromeForegroundRanges.isEmpty()) return
        if (!isRootAvailable()) return

        val copied = copyHistoryDatabase() ?: return
        try {
            val visits = readVisits(copied)
            for (visit in visits) {
                // 前面表示を確認できない訪問は自動登録しない。
                val range = chromeForegroundRanges.firstOrNull { visit.visitedAt in it } ?: continue

                // 訪問時刻から、その区間の終わりか次の訪問までを閲覧区間とみなす。
                val end = minOf(range.last, now)
                if (end <= visit.visitedAt) continue

                store.put(
                    Event.interval(
                        EventType.BROWSER_VIEW, visit.visitedAt, end,
                        JSONObject()
                            .put("url", visit.url)
                            .put("title", visit.title)
                            .put("browser_focused", true)
                            .put("source", "chrome_history_db")
                    )
                )
                lastVisitTime = maxOf(lastVisitTime, visit.rawVisitTime)
            }
        } catch (e: Exception) {
            // 構造が変わった場合など。書き込みを省略して次回に備える。
            Log.w(TAG, "Chrome の履歴を読めなかった。今回はとばす", e)
        } finally {
            copied.delete()
        }
    }

    private data class Visit(
        val url: String,
        val title: String,
        val visitedAt: Long,
        val rawVisitTime: Long,
    )

    /**
     * 履歴DBを読めるところへコピーする。
     * Chrome が開いている DB を直接触らないようにする。
     */
    private fun copyHistoryDatabase(): File? {
        val destination = File(cacheDir, "chrome_history_copy.db")
        val command = "cp '$HISTORY_DB_PATH' '${destination.absolutePath}' && " +
            "chmod 666 '${destination.absolutePath}'"
        return if (runAsRoot(command) && destination.exists()) destination else null
    }

    /**
     * urls と visits を結合して訪問を取り出す。
     *
     * Chrome の時刻は 1601-01-01 起点のマイクロ秒なので、UNIX 時刻へ直す。
     */
    private fun readVisits(database: File): List<Visit> {
        val visits = mutableListOf<Visit>()
        android.database.sqlite.SQLiteDatabase.openDatabase(
            database.absolutePath, null,
            android.database.sqlite.SQLiteDatabase.OPEN_READONLY
        ).use { db ->
            db.rawQuery(
                """
                SELECT urls.url, urls.title, visits.visit_time
                FROM visits JOIN urls ON visits.url = urls.id
                WHERE visits.visit_time > ?
                ORDER BY visits.visit_time ASC
                """.trimIndent(),
                arrayOf(lastVisitTime.toString())
            ).use { cursor ->
                while (cursor.moveToNext()) {
                    val rawVisitTime = cursor.getLong(2)
                    visits.add(
                        Visit(
                            url = cursor.getString(0) ?: continue,
                            title = cursor.getString(1).orEmpty(),
                            visitedAt = chromeTimeToEpochMillis(rawVisitTime),
                            rawVisitTime = rawVisitTime,
                        )
                    )
                }
            }
        }
        return visits
    }

    /** Chrome の時刻 (1601年起点のマイクロ秒) を UNIX ミリ秒へ直す。 */
    private fun chromeTimeToEpochMillis(chromeTime: Long): Long =
        chromeTime / 1000L - WINDOWS_EPOCH_OFFSET_MS

    private fun isRootAvailable(): Boolean = runAsRoot("id")

    private fun runAsRoot(command: String): Boolean = try {
        val process = ProcessBuilder("su", "-c", command).redirectErrorStream(true).start()
        val finished = process.waitFor()
        finished == 0
    } catch (e: Exception) {
        Log.w(TAG, "root コマンドを実行できなかった", e)
        false
    }

    companion object {
        private const val TAG = "ChromeHistory"
        private const val HISTORY_DB_PATH =
            "/data/data/com.android.chrome/app_chrome/Default/History"

        /** 1601-01-01 から 1970-01-01 までのミリ秒。 */
        private const val WINDOWS_EPOCH_OFFSET_MS = 11644473600000L
    }
}
