package com.mt3hr.gkill_autolog.collect

import android.content.Context
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * root 権限で Chrome の履歴DBを読み、閲覧を記録する。
 *
 * Chrome の履歴DBはアプリ専用領域にあるため root でしか読めない。
 * DB を直接開くとロックや WAL の問題があるので、いったんコピーしてから読む。
 * 直近のコミットは WAL 側にしか無いことがあるため、-wal と -shm も一緒にコピーする。
 *
 * 履歴には「いつ開いたか」しか無く、前面に表示していたかは分からない。
 * そのため [AppUsageCollector] が記録した Chrome の利用区間と突き合わせ、
 * **Chrome を実際に使っていた時間帯の訪問だけ**を記録する（要件 §12）。
 * 突き合わせは取り込み時の normalize では行えないため、ここで browser_focused を立てる。
 *
 * event_id は訪問そのもの（visit_time と URL）から決める。プロセスが作り直されて
 * 同じ訪問を読み直しても同じ ID になり、(端末, event_id) の重複排除で弾かれる。
 * 読んだ位置も [Config] に永続化し、読み直し自体を減らす。
 *
 * DB の構造が変わって読めなくなった場合は、何も記録せずに諦める。
 * 生ログは残っているので、修正後に取り込み直せる（要件 §12）。
 */
class ChromeHistoryCollector(
    context: Context,
    private val store: EventStore,
) {
    private val cacheDir: File = context.cacheDir
    private val config = Config(context)

    /**
     * 前回どこまで読んだか。Chrome の時刻表現で持つ。
     *
     * 進めるのは「記録するかどうかを判定できた」訪問まで。
     * まだ閉じていない Chrome 利用区間の中の訪問は判定できないので、
     * ここより後に残して次回また突き合わせる。
     */
    private var lastVisitTime: Long = config.chromeHistoryLastVisitTime

    /**
     * Chrome を前面で使っていた区間を渡すと、その時間帯の訪問だけを記録する。
     * 区間が空なら何もしない。
     *
     * su の実行と履歴DBのコピーを伴うので、主スレッドから呼んではならない。
     */
    fun collect(chromeForegroundRanges: List<LongRange>, now: Long = System.currentTimeMillis()) {
        if (chromeForegroundRanges.isEmpty()) return
        if (!isRootAvailable()) return

        if (lastVisitTime == 0L) {
            // 初回はカーソルを「突き合わせる利用区間より前」まで戻すだけにする。
            // 観測を始める前の古い履歴は、突き合わせる利用区間が存在しないので
            // どのみち1件も記録できない。カーソル 0 のまま読むと、記録できる
            // 訪問が現れるまで毎回**全履歴**をメモリへ読み込み続けてしまう。
            lastVisitTime = epochMillisToChromeTime(now - FIRST_RUN_LOOKBACK_MS)
            config.chromeHistoryLastVisitTime = lastVisitTime
        }

        val copied = copyHistoryDatabase() ?: return
        try {
            val visits = readVisits(copied)
            for ((index, visit) in visits.withIndex()) {
                // 前面表示を確認できない訪問は自動登録しない。
                val range = chromeForegroundRanges.firstOrNull { visit.visitedAt in it } ?: continue

                // 訪問時刻から、区間の終わりか次の訪問までを閲覧区間とみなす。
                // 次の訪問で必ず打ち切る。打ち切らないと、同じ区間内の訪問が
                // すべて区間の終わりまで「見ていた」ことになり重なってしまう。
                var end = minOf(range.last, now)
                visits.getOrNull(index + 1)?.let { next ->
                    end = minOf(end, next.visitedAt)
                }
                if (end <= visit.visitedAt) continue

                store.put(
                    Event.interval(
                        EventType.BROWSER_VIEW, visit.visitedAt, end,
                        JSONObject()
                            .put("url", visit.url)
                            .put("title", visit.title)
                            .put("browser_focused", true)
                            .put("source", "chrome_history_db"),
                        eventId = "browser_view:${visit.rawVisitTime}:${urlDigest(visit.url)}",
                    )
                )
                lastVisitTime = maxOf(lastVisitTime, visit.rawVisitTime)
            }
            config.chromeHistoryLastVisitTime = lastVisitTime
        } catch (e: Exception) {
            // 構造が変わった場合など。書き込みを省略して次回に備える。
            Log.w(TAG, "Chrome の履歴を読めなかった。今回はとばす", e)
        } finally {
            deleteCopies(copied)
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
     *
     * -wal が残っていると直近の訪問は本体側に無い。あれば一緒に持ってくる
     * （無い構成もあるので、こちらの失敗は無視する）。
     */
    private fun copyHistoryDatabase(): File? {
        val destination = File(cacheDir, "chrome_history_copy.db")
        // 前回の残骸を消しておく。クラッシュで deleteCopies が走らなかった場合、
        // 古いコピーが残っていると「cp が失敗したのに exists() が真」で
        // 古い履歴を読んでしまう。
        deleteCopies(destination)
        // 本体の cp が失敗したら失敗として返す。-wal / -shm は無い構成もあるので
        // そちらの失敗だけを無視する。以前は末尾の `; true` が本体の失敗まで
        // 飲み込んでいて、判定が exists() 頼みになっていた。
        val command = "if cp '$HISTORY_DB_PATH' '${destination.absolutePath}' && " +
            "chmod 666 '${destination.absolutePath}'; then " +
            "for suffix in -wal -shm; do " +
            "cp \"$HISTORY_DB_PATH\$suffix\" '${destination.absolutePath}'\$suffix 2>/dev/null && " +
            "chmod 666 '${destination.absolutePath}'\$suffix; " +
            "done; true; else false; fi"
        return if (runAsRoot(command) && destination.exists()) destination else null
    }

    private fun deleteCopies(database: File) {
        database.delete()
        File(database.absolutePath + "-wal").delete()
        File(database.absolutePath + "-shm").delete()
    }

    /**
     * urls と visits を結合して訪問を取り出す。
     *
     * Chrome の時刻は 1601-01-01 起点のマイクロ秒なので、UNIX 時刻へ直す。
     * 自分のコピーなので読み書きで開き、WAL が残っていれば取り込ませる。
     */
    private fun readVisits(database: File): List<Visit> {
        val visits = mutableListOf<Visit>()
        android.database.sqlite.SQLiteDatabase.openDatabase(
            database.absolutePath, null,
            android.database.sqlite.SQLiteDatabase.OPEN_READWRITE
        ).use { db ->
            // サブフレーム (広告 iframe 等) の自動読み込みと、Chrome が
            // 「一覧に出さない」と印を付けた URL は閲覧ではないので除く。
            // transition の下位バイトが遷移の種類で、3 = AUTO_SUBFRAME。
            // 利用者がフレーム内で自分でクリックした遷移 (MANUAL_SUBFRAME) は残す。
            db.rawQuery(
                """
                SELECT urls.url, urls.title, visits.visit_time
                FROM visits JOIN urls ON visits.url = urls.id
                WHERE visits.visit_time > ?
                  AND (visits.transition & 0xFF) != 3
                  AND urls.hidden = 0
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

    /** UNIX ミリ秒を Chrome の時刻 (1601年起点のマイクロ秒) へ直す。 */
    private fun epochMillisToChromeTime(epochMillis: Long): Long =
        (epochMillis + WINDOWS_EPOCH_OFFSET_MS) * 1000L

    /** event_id に入れる URL の識別子。同じ訪問を読み直しても同じ値になる。 */
    private fun urlDigest(url: String): String =
        MessageDigest.getInstance("SHA-256")
            .digest(url.toByteArray(Charsets.UTF_8))
            .joinToString("") { "%02x".format(it) }
            .take(16)

    private fun isRootAvailable(): Boolean = runAsRoot("id")

    /**
     * root でコマンドを実行する。
     *
     * su マネージャが確認ダイアログを出す設定だと待ちが終わらないことがあるため、
     * 上限を付けて打ち切る。打ち切ったら失敗として扱い、次回に任せる。
     */
    private fun runAsRoot(command: String): Boolean = try {
        val process = ProcessBuilder("su", "-c", command).redirectErrorStream(true).start()
        if (!process.waitFor(ROOT_COMMAND_TIMEOUT_SECONDS, TimeUnit.SECONDS)) {
            process.destroyForcibly()
            Log.w(TAG, "root コマンドが時間内に終わらなかったため打ち切った")
            false
        } else {
            process.exitValue() == 0
        }
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

        /** root コマンドの待ち時間の上限（秒）。 */
        private const val ROOT_COMMAND_TIMEOUT_SECONDS = 15L

        /**
         * 初回にどこまで遡って読むか。
         * 突き合わせる利用区間 (AppUsageCollector の直近の観測) を確実に覆う長さ。
         */
        private const val FIRST_RUN_LOOKBACK_MS = 60 * 60 * 1000L
    }
}
