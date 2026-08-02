package com.mt3hr.gkill_autolog

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 端末の再起動後とアプリの更新後に収集を再開する。
 *
 * 更新を拾うのは、そうしないと更新のたびに収集が止まったままになるため。
 * Android はアプリを入れ替えるとそのプロセスを停止するが、
 * 常駐サービスは自動では戻らない。気付かないまま記録が途切れるので、
 * 更新完了時に届く MY_PACKAGE_REPLACED でも開始する。
 *
 * 書き出し先の許可が無くても収集は始める。
 * 生ログは端末内に溜まり、許可されたあとの書き出しでまとめて渡される。
 *
 * 利用者が「収集を停止」していた場合は再開しない。
 * 止めたつもりの期間のログが再起動や更新で勝手に残らないようにする。
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED -> AutologService.startIfEnabled(context)
        }
    }
}
