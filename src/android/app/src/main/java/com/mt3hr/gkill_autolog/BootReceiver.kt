package com.mt3hr.gkill_autolog

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 再起動後に収集を再開する。
 *
 * 書き出し先の許可が無くても収集は始める。
 * 生ログは端末内に溜まり、許可されたあとの書き出しでまとめて渡される。
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        AutologService.start(context)
    }
}
