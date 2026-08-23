package com.mt3hr.gkill_autolog.collect

import android.app.KeyguardManager
import android.content.Context
import android.os.PowerManager

/**
 * 端末が使われている状態か（画面が点いていて、ロック画面が出ていないか）。
 *
 * スクリーンショットと音声の両方が同じ判定を要る。別々に書くと、
 * 片方だけ直したときに食い違う。位置情報の権限判定を
 * [LocationCollector.hasLocationPermission] へ寄せたのと同じ理由。
 */
class ScreenState(context: Context) {

    private val powerManager: PowerManager? =
        context.getSystemService(Context.POWER_SERVICE) as? PowerManager

    private val keyguardManager: KeyguardManager? =
        context.getSystemService(Context.KEYGUARD_SERVICE) as? KeyguardManager

    /**
     * いま端末が使われているか。
     *
     * 画面が消えている間は false。ロック画面が出ている間も false。
     * どちらも取れない端末では false にする。使われていると決めつけない。
     */
    fun isInUse(): Boolean {
        if (powerManager?.isInteractive != true) return false
        if (keyguardManager?.isKeyguardLocked == true) return false
        return true
    }
}
