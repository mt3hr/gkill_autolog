package com.mt3hr.gkill_autolog

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 録音中の通知の「録音を停止」を受ける。
 *
 * 外向けの [RecordingReceiver] とは分けてある。こちらは exported="false" で、
 * 自分の通知の PendingIntent からしか届かない。そのため外部からの操作の
 * 設定にかかわらず止められる（設定画面のボタンと同じ扱い）。
 */
class RecordingStopReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        ManualRecording.stop(context, ManualRecording.Source.APP)
    }
}
