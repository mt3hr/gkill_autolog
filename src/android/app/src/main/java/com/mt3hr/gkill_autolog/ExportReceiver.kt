package com.mt3hr.gkill_autolog

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import com.mt3hr.gkill_autolog.export.JsonlExporter

/**
 * 外から書き出しをさせるための受け口。
 *
 * Termux の autolog.sh が取り込みの直前にこれを叩く。
 * 収集した記録はアプリ内の DB に溜まっていて、書き出すまで
 * /sdcard/gkill_autolog/events には出てこない。叩かないと
 * 「取り込んだが直近の分が入っていない」状態になる。
 *
 *   am broadcast --user 0 -f 0x20 -n com.mt3hr.gkill_autolog/.ExportReceiver
 *
 * (--user 0 はシェルからの明示。-f 0x20 = FLAG_INCLUDE_STOPPED_PACKAGES で、
 * 強制停止状態のアプリにも届ける。operations-guide.md の実運用形と揃えてある)
 *
 * intent-filter は付けない。呼ぶ側にコンポーネント名を明示させることで、
 * 他のアプリのブロードキャストにたまたま反応することがなくなる。
 */
class ExportReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        // 書き出しはファイル入出力なので main スレッドでやらない。
        // goAsync() で結果を保留すると、finish() を呼ぶまで
        // ブロードキャストが生きたままになる。am broadcast は
        // そこまで待ってから結果を表示するので、呼び出し側は
        // 書き出しの完了を待てる。
        val pending = goAsync()
        val appContext = context.applicationContext

        Thread {
            var code = RESULT_FAILED
            var message = "書き出せなかった"
            try {
                val exported = JsonlExporter(appContext).export()
                code = exported
                message = "$exported 件を書き出した"
                Log.i(TAG, message)
            } catch (e: Exception) {
                // 全ファイルアクセスの許可が落ちているときにここへ来る。
                Log.e(TAG, "書き出しに失敗した", e)
            } finally {
                pending.setResult(code, message, null)
                pending.finish()
            }
        }.start()
    }

    companion object {
        // ログタグは他の書き出し系と同じ Autolog 接頭辞で揃える (logcat でまとめて絞るため)。
        private const val TAG = "AutologExport"

        /** 書き出せなかったときの結果コード。成功時は書き出した件数を返す。 */
        const val RESULT_FAILED = -1
    }
}
