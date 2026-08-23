package com.mt3hr.gkill_autolog.collect

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.SharedStorage
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.TimeUnit

/**
 * 決まった間隔でスクリーンショットを撮る。
 *
 * 撮影時刻は間隔で丸める。60分なら毎時00分、15分なら毎時00分・15分・30分・45分。
 * 間隔は設定画面から変えられる。
 *
 * root の `screencap` を使う。MediaProjection API でも撮れるが、
 * 起動のたびに「画面の記録を開始しますか」の確認ダイアログが出るため、
 * 無人で撮り続ける用途には向かない。
 *
 * 画面が消えている間とロック中は撮らない（Windows 側と同じ扱い。要件 §10）。
 *
 * ただしスマホは大半の時間で画面が消えているので、区切りの時刻ちょうどを
 * 待っているとほとんど撮れない。「撮り逃したら次に画面を点けたときに撮る」を
 * 入れておくと、その区切りのぶんを画面が点いた時点で撮る。
 *
 * このとき記録するのは**実際に撮れた時刻**で、区切りの時刻ではない。
 * 撮れなかった時間の画像をでっち上げないため。
 * 「撮り逃した時間の画像を後から補完しない」は守られている。
 *
 * 撮った画像は共有ストレージへ置くだけ。そこから先へ運ぶのは
 * 同期スクリプト (gkill_server dvnf) の役目で、AutoScreenshot_<端末>_<日付> にまとめられる。
 */
class ScreenshotCollector(
    private val context: Context,
    private val config: Config,
) {
    private val screen = ScreenState(context)

    /** 直近に処理した区切り。同じ区切りで二度撮らないために持つ。 */
    private var lastCapturedBucket: Long = 0

    /** 直近に使った撮影間隔。設定が変わったことに気づくために持つ。 */
    private var lastIntervalMs: Long = 0

    /**
     * 画面が消えていて撮れなかった区切り。0 なら借りは無い。
     *
     * 画面が点いたときにここを見て撮り直す。持つのは最後の1つだけ。
     * 何時間も消えていたあとに、その間の回数ぶんまとめて撮っても仕方がない。
     */
    private var pendingBucket: Long = 0

    /**
     * 撮影時刻を過ぎていれば1枚撮る。サービスから定期的に呼ぶ。
     * root コマンドの実行を伴うので、必ず別スレッドから呼ぶこと。
     */
    fun captureIfDue(now: Long = System.currentTimeMillis()) {
        if (!config.captureScreenshots) return

        val intervalMs = config.screenshotIntervalMinutes * MINUTE_MS

        // 間隔を変えた直後は、前の間隔で決めた区切りが残っている。
        // そのままだと新しい間隔での最初の1回が飛ぶので、区切り直す。
        if (intervalMs != lastIntervalMs) {
            lastIntervalMs = intervalMs
            lastCapturedBucket = 0
            pendingBucket = 0
        }

        val bucket = now / intervalMs * intervalMs
        val ready = canCaptureNow()

        if (bucket != lastCapturedBucket) {
            lastCapturedBucket = bucket

            if (ready) {
                // 区切りの時刻に撮れた。記録時刻も区切りに合わせる。
                pendingBucket = 0
                capture(bucket)
                return
            }

            // 画面が消えているか、ロック画面が出ている。
            // 設定が入っていれば、次に画面を点けたときに撮り直す。
            pendingBucket = if (config.captureOnUnlock) bucket else 0
            return
        }

        // 撮り逃した区切りの借りを、画面が点いた時点で返す。
        //
        // 記録時刻は区切りではなく**実際に撮れた時刻**にする。
        // 区切りの時刻を名乗らせると、撮れていない時間の画像を
        // でっち上げることになるため。
        if (pendingBucket != 0L && ready) {
            pendingBucket = 0
            Log.i(TAG, "撮り逃した区切りのぶんを、画面が点いたので撮る")
            capture(now)
        }
    }

    /**
     * いま撮ってよいか。
     *
     * 画面が消えている間は撮らない。ロック画面が出ている間も撮らない。
     * ロック画面を撮っても中身が無く、Windows 側もロック中は撮らない（要件 §10）。
     */
    private fun canCaptureNow(): Boolean = screen.isInUse()

    private fun capture(capturedAt: Long) {
        if (!SharedStorage.prepare(SharedStorage.screenshotsDir)) {
            Log.w(TAG, "共有ストレージへ書けないため撮らない。全ファイルアクセスの許可が要る")
            return
        }
        if (config.device.isBlank()) {
            // ファイル名が <端末名>_<時刻>.webp なので、端末名が無いと後で判別できない。
            Log.w(TAG, "端末名が決まっていないため撮らない。アプリの設定で端末名を入れること")
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
            //
            // 書きかけを同期スクリプトに拾われないよう、JSONL や GPX と同じく
            // 一時名で書いてから rename する。WebP への圧縮は時間がかかるので、
            // 最終名へ直接書くと壊れた画像が運ばれることがある。
            val destination = File(SharedStorage.screenshotsDir, name)
            val temporary = File(SharedStorage.screenshotsDir, "$name.tmp")
            temporary.outputStream().use { out ->
                @Suppress("DEPRECATION")
                val format = if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.R) {
                    Bitmap.CompressFormat.WEBP_LOSSLESS
                } else {
                    Bitmap.CompressFormat.WEBP
                }
                bitmap.compress(format, 100, out)
                out.fd.sync()
            }
            bitmap.recycle()

            // IDF は mtime を RelatedTime にする。撮影時刻に合わせておかないと
            // 取り込んだ日時で記録されてしまう。rename より先に合わせておけば、
            // 現れた瞬間から正しい mtime を持ち、運ばれるのと競合しない。
            if (!temporary.setLastModified(capturedAt)) {
                Log.w(TAG, "撮影時刻を mtime に反映できなかった: ${temporary.name}")
            }
            if (!temporary.renameTo(destination)) {
                Log.w(TAG, "撮ったファイルの名前を変えられなかった: ${temporary.name}")
                temporary.delete()
                return
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

    /**
     * root でコマンドを実行する。
     *
     * su マネージャが確認ダイアログを出す設定だと待ちが終わらないことがある。
     * 撮影は単一の ioExecutor で動くため、ここで無期限に待つと
     * JSONL の書き出し・GPX・Chrome 履歴の収集まで全部が止まる。
     * ChromeHistoryCollector と同じ上限で打ち切り、失敗として次回に任せる。
     */
    private fun runAsRoot(command: String): Boolean = try {
        val process = ProcessBuilder("su", "-c", command).redirectErrorStream(true).start()
        if (!process.waitFor(ROOT_COMMAND_TIMEOUT_SECONDS, TimeUnit.SECONDS)) {
            process.destroyForcibly()
            Log.w(TAG, "root コマンドが時間内に終わらなかったため打ち切った")
            false
        } else {
            process.exitValue() == 0
        }
    } catch (e: Exception) {
        Log.w(TAG, "root コマンドを実行できなかった", e)
        false
    }

    companion object {
        private const val TAG = "AutologScreenshot"
        private const val MINUTE_MS = 60 * 1000L

        /** root コマンドの待ち時間の上限（秒）。ChromeHistoryCollector と揃えてある。 */
        private const val ROOT_COMMAND_TIMEOUT_SECONDS = 15L
    }
}
