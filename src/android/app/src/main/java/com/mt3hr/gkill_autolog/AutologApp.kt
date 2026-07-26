package com.mt3hr.gkill_autolog

import android.app.Application
import android.os.Build
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * アプリの入口。落ちた理由を残す仕掛けをここで入れる。
 */
class AutologApp : Application() {

    override fun onCreate() {
        super.onCreate()
        installCrashLogger()
    }

    /**
     * 落ちた理由を共有ストレージへ書き残す。
     *
     * このアプリは端末に置きっぱなしで動かすので、
     * 落ちた瞬間に PC へ繋いで logcat を見られるとは限らない。
     * ADB が無くても後から原因を追えるようにしておく。
     *
     * 書き残したあとは、これまでどおり既定の処理へ渡してプロセスを終わらせる。
     * 握り潰すと、壊れた状態のまま動き続けることになる。
     */
    private fun installCrashLogger() {
        val previous = Thread.getDefaultUncaughtExceptionHandler()

        Thread.setDefaultUncaughtExceptionHandler { thread, error ->
            runCatching { writeCrashLog(thread, error) }
            previous?.uncaughtException(thread, error)
        }
    }

    private fun writeCrashLog(thread: Thread, error: Throwable) {
        Log.e(TAG, "落ちた (${thread.name})", error)

        if (!SharedStorage.prepare(SharedStorage.root)) return

        val at = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date())
        val report = buildString {
            appendLine("---- $at ----")
            appendLine("スレッド: ${thread.name}")
            appendLine("端末: ${Build.MANUFACTURER} ${Build.MODEL} / Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})")
            appendLine(Log.getStackTraceString(error))
            appendLine()
        }

        // 追記していく。前回の分も残す。
        val file = File(SharedStorage.root, CRASH_LOG_NAME)
        file.appendText(report)
    }

    companion object {
        private const val TAG = "AutologApp"

        /** 落ちた理由を書き残すファイル。/sdcard/gkill_autolog/crash.log */
        const val CRASH_LOG_NAME = "crash.log"
    }
}
