package com.mt3hr.gkill_autolog.collect

import android.accessibilityservice.AccessibilityService
import android.view.accessibility.AccessibilityEvent

/**
 * 前面アプリの把握を補う。
 *
 * 主たる情報源は UsageStatsManager で、こちらは補助（要件 §8.2 の取得優先順位）。
 * 画面の中身は読み取らず、どのアプリが前面になったかだけを覚える。
 *
 * ここでは生ログを書かない。書くと UsageStats と二重になるため、
 * 前面アプリの現在値を保持するだけにして、必要な収集側が参照する。
 */
class AutologAccessibilityService : AccessibilityService() {

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        val accessibilityEvent = event ?: return
        if (accessibilityEvent.eventType != AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED) return

        val packageName = accessibilityEvent.packageName?.toString() ?: return
        foregroundPackage = packageName
        foregroundSince = System.currentTimeMillis()
    }

    override fun onInterrupt() {
        // 何もしない。
    }

    override fun onDestroy() {
        super.onDestroy()
        foregroundPackage = null
    }

    companion object {
        /** 直近に前面になったアプリ。取得できていなければ null。 */
        @Volatile
        var foregroundPackage: String? = null
            private set

        /** 前面になった時刻。 */
        @Volatile
        var foregroundSince: Long = 0
            private set
    }
}
