package com.mt3hr.gkill_autolog.export

// 編集前に読む: .claude/skills/autolog-android/SKILL.md（この領域の不変条件の正本）

import android.content.Context
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.SharedStorage
import com.mt3hr.gkill_autolog.store.EventStore
import java.io.File
import java.util.concurrent.atomic.AtomicLong

/**
 * 溜まった生ログを共有ストレージへ JSONL として書き出す。
 *
 * 以前は PC の受け口へ HTTP で送っていたが、
 * 取り込みが端末内で完結するようになったので送信は要らなくなった。
 * 書き出した分だけを端末から消すのは送信していた頃と同じで、
 * 書き出せなければ端末に残って次回やり直される（要件 §17）。
 *
 * 書きかけを autolog に読まれないよう、いったん .jsonl.tmp へ書いてから
 * .jsonl へ rename する。autolog は .jsonl だけを読む。
 */
class JsonlExporter(context: Context) {

    private val config = Config(context)
    private val store = EventStore(context.applicationContext)

    /**
     * 書き出しを試みる。書き出せた件数を返す。
     *
     * 常駐サービスと [ExportWorker] の両方から呼ばれるため、プロセス内で排他する。
     * take と delete の間に別のスレッドが同じ行を取ると、
     * 同じ内容の JSONL が二重に書き出される（実際に起きた）。
     * 取り込み側は event_id で弾くので実害は出ないが、無駄なので防ぐ。
     */
    fun export(): Int = synchronized(exportLock) {
        if (!SharedStorage.prepare(SharedStorage.eventsDir)) {
            Log.w(TAG, "共有ストレージへ書けないため書き出さない。全ファイルアクセスの許可が要る")
            return 0
        }
        if (config.device.isBlank()) {
            // 端末名はファイル名と gkill の端末名になる。空のまま書き出すと後で直せない。
            Log.w(TAG, "端末名が決まっていないため書き出さない。アプリの設定で端末名を入れること")
            return 0
        }

        var exported = 0
        while (true) {
            val stored = store.take(EventStore.EXPORT_BATCH_SIZE)
            if (stored.isEmpty()) break

            val lines = StringBuilder()
            for (item in stored) {
                // JSONObject.toString() は改行を出さないので1件1行になる。
                lines.append(item.event.toJson(config.device).toString()).append('\n')
            }

            if (!writeBatch(lines.toString())) {
                // 書けなかった分は残したまま次回に回す。
                break
            }
            store.delete(stored.map { it.rowId })
            exported += stored.size

            if (stored.size < EventStore.EXPORT_BATCH_SIZE) break
        }
        return exported
    }

    /** 書き出していない件数。 */
    fun pendingCount(): Int = store.pendingCount()

    private fun writeBatch(content: String): Boolean {
        // 名前が既存ファイルと重なると rename が黙って上書きし、
        // 書き出し済み（＝端末からは削除済み）の生ログが失われる。
        // 時計の巻き戻りや同一ミリ秒でも重ならないよう、連番を足したうえで
        // 既存の名前を避ける。
        val stamp = System.currentTimeMillis()
        var temporary: File
        var destination: File
        do {
            val base = "${config.device}-$stamp-${sequence.incrementAndGet()}"
            temporary = File(SharedStorage.eventsDir, "$base.jsonl.tmp")
            destination = File(SharedStorage.eventsDir, "$base.jsonl")
        } while (destination.exists() || temporary.exists())

        return try {
            temporary.outputStream().use { out ->
                out.write(content.toByteArray(Charsets.UTF_8))
                // rename の前に確実に書き終える。
                out.fd.sync()
            }
            if (!temporary.renameTo(destination)) {
                Log.w(TAG, "書き出したファイルの名前を変えられなかった: ${temporary.name}")
                temporary.delete()
                return false
            }
            Log.i(TAG, "書き出した: ${destination.name}")
            true
        } catch (e: Exception) {
            Log.w(TAG, "書き出せなかった。次回やり直す: ${e.message}")
            temporary.delete()
            false
        }
    }

    companion object {
        private const val TAG = "AutologExporter"

        /**
         * 書き出しの排他。
         *
         * 呼び出し元ごとに JsonlExporter を作るので、インスタンスではなくここで持つ。
         */
        private val exportLock = Any()

        /** 同じミリ秒に複数バッチを書いても名前が重ならないための連番。 */
        private val sequence = AtomicLong(0)
    }
}
