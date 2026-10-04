package com.mt3hr.gkill_autolog

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * 外から手動録音を操作させるための受け口。Tasker から叩く。
 *
 *   am broadcast -a com.mt3hr.gkill_autolog.TOGGLE_RECORDING -n com.mt3hr.gkill_autolog/.RecordingReceiver
 *
 * アクションは [ACTION_START]・[ACTION_STOP]・[ACTION_TOGGLE] の3つ。
 * 音量ボタンの長押しのように1つの操作で始めて止めたいときは TOGGLE を使う。
 *
 * **設定でオンにしていないと受け付けない（既定はオフ）。** 誰でも叩ける受け口なので、
 * 入れただけで他のアプリが録音を始められる経路にしない。録音中は通知と
 * トーストで必ず見えるようにしてある（[ManualRecording]）。
 *
 * **ここから常駐を起こしてマイクを掴み直さない。** マイク種別の前景サービスは
 * アプリが前に出ている間しか始められず、ブロードキャストが届くのはたいてい背景にいるとき。
 * 背景から startForegroundService でマイクを宣言し直すと、その場で例外になる。
 * 常駐が既に掴んでいるマイクの上で録り、掴めていなければ理由をトーストに出して断る。
 *
 * intent-filter は付けない（[ExportReceiver] と同じ）。呼ぶ側にコンポーネント名を
 * 明示させることで、他のアプリのブロードキャストにたまたま反応することがなくなる。
 */
class RecordingReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val reply = when (intent.action) {
            ACTION_START -> ManualRecording.start(context, ManualRecording.Source.EXTERNAL)
            ACTION_STOP -> ManualRecording.stop(context, ManualRecording.Source.EXTERNAL)
            ACTION_TOGGLE -> ManualRecording.toggle(context, ManualRecording.Source.EXTERNAL)
            else -> ManualRecording.Reply(false, "知らない操作: ${intent.action}")
        }
        Log.i(TAG, reply.message)

        // am broadcast（順序付き）で叩いたときだけ結果を返す。Tasker の送り方は
        // 順序付きにならず、そこで結果を書くとエラーがログに出る。
        if (isOrderedBroadcast) {
            setResult(if (reply.accepted) RESULT_ACCEPTED else RESULT_REFUSED, reply.message, null)
        }
    }

    companion object {
        // ログタグは手動録音の他の部分と揃える (logcat でまとめて絞るため)。
        private const val TAG = "AutologRecording"

        /** 録音を始める。録音中なら何もしない。 */
        const val ACTION_START = "com.mt3hr.gkill_autolog.START_RECORDING"

        /** 録音を止める。止まっていれば何もしない。 */
        const val ACTION_STOP = "com.mt3hr.gkill_autolog.STOP_RECORDING"

        /** 録音中なら止め、止まっていれば始める。 */
        const val ACTION_TOGGLE = "com.mt3hr.gkill_autolog.TOGGLE_RECORDING"

        /** 受け付けたときの結果コード。 */
        const val RESULT_ACCEPTED = 1

        /** 断ったときの結果コード。理由は結果データの文字列に入る。 */
        const val RESULT_REFUSED = 0
    }
}
