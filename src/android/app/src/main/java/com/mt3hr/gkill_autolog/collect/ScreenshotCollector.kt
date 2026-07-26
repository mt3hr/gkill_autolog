package com.mt3hr.gkill_autolog.collect

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.os.PowerManager
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.SharedStorage
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 毎時00分にスクリーンショットを撮る。
 *
 * root の `screencap` を使う。MediaProjection API でも撮れるが、
 * 起動のたびに「画面の記録を開始しますか」の確認ダイアログが出るため、
 * 毎時無人で撮る用途には向かない。
 *
 * 画面が消えている間は撮らない。撮り逃した時間の画像を後から補完もしない
 * （Windows 側と同じ扱い。要件 §10）。
 *
 * 撮った画像は共有ストレージへ置くだけ。そこから先へ運ぶのは
 * termux-tasker の dvnf.sh の役目で、AutoScreenshot_<端末>_<日付> にまとめられる。
 */
class ScreenshotCollector(
    private val context: Context,
    private val config: Config,
) {
    private val powerManager: PowerManager? =
        context.getSystemService(Context.POWER_SERVICE) as? PowerManager

    /** 直近に撮った正時。同じ時刻で二度撮らないために持つ。 */
    private var lastCapturedHour: Long = 0

    /**
     * 正時を過ぎていれば1枚撮る。サービスから定期的に呼ぶ。
     * root コマンドの実行を伴うので、必ず別スレッドから呼ぶこと。
     */
    fun captureIfDue(now: Long = System.currentTimeMillis()) {
        if (!config.captureScreenshots) return

        val hour = now / HOUR_MS * HOUR_MS
        if (hour == lastCapturedHour) return

        // 画面が消えている間は撮らない。
        if (powerManager?.isInteractive != true) {
            // 撮らなかった正時も記録しておく。復帰した瞬間に撮ると
            // 正時から離れた画像になるため、その時間はあきらめる。
            lastCapturedHour = hour
            return
        }

        lastCapturedHour = hour
        capture(hour)
    }

    private fun capture(capturedAt: Long) {
        if (!SharedStorage.prepare(SharedStorage.screenshotsDir)) {
            Log.w(TAG, "共有ストレージへ書けないため撮らない。全ファイルアクセスの許可が要る")
            return
        }

        val name = fileName(capturedAt)
        val pngPath = File(context.cacheDir, "screencap.png")

        if (!runAsRoot("screencap -p '${pngPath.absolutePath}' && chmod 666 '${pngPath.absolutePath}'")) {
            Log.w(TAG, "screencap を実行できなかった。root が無いか許可されていない")
            return
        }
        if (!pngPath.exists() || pngPath.length() == 0L) {
            Log.w(TAG, "screencap がファイルを作らなかった")
            return
        }

        try {
            val bitmap = BitmapFactory.decodeFile(pngPath.absolutePath)
            if (bitmap == null) {
                Log.w(TAG, "撮った画像を読めなかった")
                return
            }
            // Windows 側と同じく可逆 WebP にする。PNG より小さい。
            val destination = File(SharedStorage.screenshotsDir, name)
            destination.outputStream().use { out ->
                @Suppress("DEPRECATION")
                val format = if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.R) {
                    Bitmap.CompressFormat.WEBP_LOSSLESS
                } else {
                    Bitmap.CompressFormat.WEBP
                }
                bitmap.compress(format, 100, out)
            }
            bitmap.recycle()

            // IDF は mtime を RelatedTime にする。撮影時刻に合わせておかないと
            // 取り込んだ日時で記録されてしまう。
            if (!destination.setLastModified(capturedAt)) {
                Log.w(TAG, "撮影時刻を mtime に反映できなかった: ${destination.name}")
            }

            Log.i(TAG, "スクリーンショットを保存した: ${destination.name} (${destination.length()} bytes)")
        } catch (e: Exception) {
            Log.w(TAG, "スクリーンショットの変換に失敗した", e)
        } finally {
            pngPath.delete()
        }
    }

    /** 例: <端末名>_2026-07-26_12-00-00.webp （Windows 側と同じ形） */
    private fun fileName(capturedAt: Long): String {
        val stamp = SimpleDateFormat("yyyy-MM-dd_HH-mm-ss", Locale.US).format(Date(capturedAt))
        return "${config.device}_$stamp.webp"
    }

    private fun runAsRoot(command: String): Boolean = try {
        val process = ProcessBuilder("su", "-c", command).redirectErrorStream(true).start()
        process.waitFor() == 0
    } catch (e: Exception) {
        Log.w(TAG, "root コマンドを実行できなかった", e)
        false
    }

    companion object {
        private const val TAG = "AutologScreenshot"
        private const val HOUR_MS = 60 * 60 * 1000L
    }
}
