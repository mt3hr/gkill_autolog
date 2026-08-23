package com.mt3hr.gkill_autolog.collect

import android.content.Context
import android.media.MediaRecorder
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.SharedStorage
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 決まった間隔で、決まった長さの音声を録る。
 *
 * 録音時刻は間隔で丸める。1時間なら毎時00分、3時間なら00時・03時・06時…。
 * 間隔と長さは設定画面から変えられる。
 *
 * 既定では画面が消えている間とロック中も録る。スクリーンショットと違い、
 * 画面が消えていても記録すべき事実があるため。設定で
 * 「端末を使っている間だけ」にもできる。
 *
 * **撮り逃した区切りをあとで録り直すことはしない。** スクリーンショットには
 * 「撮り逃したら次に画面を点けたときに撮る」があるが、あとで録った音は
 * 別の時刻の音であって、その区切りの音ではない。録れなかった時間の音を
 * でっち上げないため、区切りを逃したらそのまま空ける。
 *
 * 録った音は共有ストレージへ置くだけ。そこから先へ運ぶのは
 * 同期スクリプト (gkill_server dvnf) の役目で、AutoAudio_<端末>_<日付> にまとめられる。
 *
 * **常駐サービスの ioExecutor は使わない。** あちらは単一スレッドで、
 * スクリーンショット・Chrome 履歴・GPX・生ログの書き出しが全部載っている。
 * 数分の録音でそこを塞ぐと、収集が丸ごと止まる。
 */
class AudioCollector(
    private val context: Context,
    private val config: Config,
) {
    private val screen = ScreenState(context)

    /**
     * 録音を回すスレッド。MediaRecorder はこの上で作る（後述）。
     *
     * 立ち上げと畳みは主スレッド（[recordIfDue] と [stop]）、
     * 読むのは録音スレッドもなので @Volatile を付ける。
     */
    @Volatile
    private var thread: HandlerThread? = null

    @Volatile
    private var handler: Handler? = null

    /** 録音中かどうか。区切りが重なっても二重に始めない。 */
    private val recording = AtomicBoolean(false)

    /** 停止処理に入ったかどうか。時間切れの通知と自前のタイマーの両方から来るため。 */
    private val stopping = AtomicBoolean(false)

    private var recorder: MediaRecorder? = null

    /** いま録っている音の開始時刻。ファイル名と mtime になる。 */
    private var startedAt: Long = 0

    /** 直近に処理した区切り。同じ区切りで二度録らないために持つ。 */
    private var lastBucket: Long = 0

    /** 直近に使った録音間隔。設定が変わったことに気づくために持つ。 */
    private var lastIntervalMs: Long = 0

    /**
     * 録音の時刻を過ぎていれば録り始める。サービスから定期的に呼ぶ。
     *
     * 区切りの判定だけをこの場で行い、録音そのものは専用スレッドへ渡す。
     * 主スレッドから呼んでよい。
     */
    fun recordIfDue(now: Long = System.currentTimeMillis()) {
        if (!config.recordAudio) {
            stop()
            return
        }

        val intervalMs = config.audioIntervalMinutes * MINUTE_MS
        val bucket = now / intervalMs * intervalMs

        // 始めたときと間隔を変えたときは、**いま入っている区切りを消費済みにする。**
        //
        // ここを 0 に戻すと、その場ですぐ録り始めてしまう。録音の時刻には
        // 区切りの時刻を使うので、12:47 に始めた録音が「12:00 の録音」として
        // 置かれることになる。IDF はファイルの更新時刻を記録時刻にするので、
        // gkill には最大で間隔ぶんずれた事実が残る。
        //
        // その区切りは頭から録れていない以上、録り逃したものとして空ける。
        // 次の区切りの頭から録る。
        if (intervalMs != lastIntervalMs) {
            lastIntervalMs = intervalMs
            lastBucket = bucket
            return
        }

        if (bucket == lastBucket) return
        lastBucket = bucket

        if (!config.recordAudioWhileScreenOff && !screen.isInUse()) {
            // 端末を使っている間だけ録る設定。この区切りは空ける。
            return
        }
        if (recording.get()) {
            // 前の録音がまだ終わっていない。長さが間隔を超えているときに起こる。
            Log.w(TAG, "前の録音が続いているため、この区切りは録らない")
            return
        }

        val durationMs = Config
            .clampAudioDurationMinutes(config.audioDurationMinutes, config.audioIntervalMinutes)
            .toLong() * MINUTE_MS

        handler().post { start(bucket, durationMs) }
    }

    /**
     * 録音を止めて、録れた分を確定させる。
     *
     * 主スレッド（サービスの onDestroy）から呼ばれる。ここで待つと ANR になるので、
     * 停止は録音スレッドへ渡すだけにして戻る。
     */
    fun stop() {
        val existing = thread ?: return
        handler?.post { finish() }
        // 停止を積んだあとに畳む。先に畳むと、積んだ処理の行き先が無くなる。
        existing.quitSafely()
        thread = null
        handler = null
    }

    /** 録音スレッド。無ければ立ち上げる。 */
    private fun handler(): Handler {
        handler?.let { return it }
        val started = HandlerThread(THREAD_NAME).apply { start() }
        thread = started
        return Handler(started.looper).also { handler = it }
    }

    /**
     * 録り始める。**必ず録音スレッドから呼ぶこと。**
     *
     * MediaRecorder は**作ったときの Looper** へコールバックを配る。
     * 主スレッドで作ると、時間切れの通知も stop も主スレッドに来てしまう。
     */
    private fun start(bucket: Long, durationMs: Long) {
        if (!recording.compareAndSet(false, true)) return

        if (config.device.isBlank()) {
            // ファイル名が <端末名>_<時刻>.m4a なので、端末名が無いと後で判別できない。
            fail("端末名が決まっていない。アプリの設定で端末名を入れること")
            return
        }

        // 録るのはアプリのキャッシュ。共有ストレージへ直に録らないのは2つ理由がある。
        // MPEG_4 は停止するときに moov atom を先頭へ書き戻すのでシークできる先が要ること、
        // 録音中ずっと育ちかけのファイルが置き場に見えて、同期スクリプトに
        // 録りかけを掴まれること。
        //
        // 名前は区切りごとに変える。サービスが作り直されると AudioCollector も
        // 別物になるので、固定名にすると、前のサービスの書き出しと
        // 新しいサービスの録音が同じファイルを取り合う。
        startedAt = bucket
        val working = workingFile()
        working.delete()

        // prepare や start が投げたときに release できるよう、try の外で持つ。
        // 掴んだままにすると、マイクが塞がって次の区切りも失敗し続ける。
        @Suppress("DEPRECATION")
        val target = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            MediaRecorder(context)
        } else {
            MediaRecorder()
        }

        try {
            target.setAudioSource(MediaRecorder.AudioSource.MIC)
            target.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
            target.setAudioEncoder(MediaRecorder.AudioEncoder.AAC)
            target.setAudioChannels(CHANNELS)
            target.setAudioSamplingRate(SAMPLE_RATE_HZ)
            target.setAudioEncodingBitRate(BIT_RATE)
            // 自前のタイマーが主で、これは保険。両方来ても finish が1回に畳む。
            target.setMaxDuration(durationMs.toInt())
            target.setOnInfoListener { _, what, _ ->
                if (what == MediaRecorder.MEDIA_RECORDER_INFO_MAX_DURATION_REACHED) finish()
            }
            target.setOnErrorListener { _, what, extra ->
                Log.w(TAG, "録音中にエラーが起きた ($what, $extra)")
                abort()
            }
            target.setOutputFile(working.absolutePath)
            target.prepare()
            target.start()
        } catch (e: Exception) {
            // 他のアプリがマイクを使っている、通話中、権限が無い、など。
            // 記録は残さず、次の区切りでやり直す。
            runCatching { target.release() }
            working.delete()
            fail("録音を始められなかった: ${e.message}")
            return
        }

        // 振幅の記録を空にしておく。getMaxAmplitude は前の呼び出しからの
        // 最大値を返すもので、**最初の1回は 0 を返す。** ここで捨てておかないと、
        // 停止の直前に読む値がいつも 0 になり、無音の検出が働かない。
        runCatching { target.getMaxAmplitude() }

        recorder = target
        stopping.set(false)
        handler?.postDelayed({ finish() }, durationMs)
        Log.i(TAG, "録音を始めた (${durationMs / MINUTE_MS} 分)")
    }

    /**
     * 録り終えて、共有ストレージへ出す。
     *
     * 時間切れの通知と自前のタイマーの両方から来るので、1回に畳む。
     */
    private fun finish() {
        if (!recording.get()) return
        if (!stopping.compareAndSet(false, true)) return

        // 残っているタイマー（保険と本命のうち、来なかったほう）を落とす。
        handler?.removeCallbacksAndMessages(null)

        val current = recorder
        recorder = null

        // 停止の直前に一度だけ読む。前の呼び出しからの最大値を返すので、
        // 1回で録音の全体をカバーする。
        val amplitude = runCatching { current?.getMaxAmplitude() ?: 0 }.getOrDefault(0)

        val stopped = try {
            current?.stop()
            true
        } catch (e: Exception) {
            // 録音が極端に短いとここへ来る。このとき出力は壊れている。
            Log.w(TAG, "録音を止められなかった。この分は捨てる: ${e.message}")
            false
        }
        runCatching { current?.release() }

        val working = workingFile()
        if (!stopped || working.length() == 0L) {
            working.delete()
            recording.set(false)
            stopping.set(false)
            return
        }

        if (amplitude == 0) {
            // 静かな部屋も、塞がれたマイクも、同じ無音になる。区別できないので、
            // 気づけるように残すだけにして、ファイルは捨てない。
            Log.w(TAG, "録音の全体が無音だった。マイクが塞がれている可能性がある")
        }

        publish(working)
        recording.set(false)
        stopping.set(false)
    }

    /** エラーで打ち切る。録れた分は当てにならないので出さない。 */
    private fun abort() {
        if (!recording.get()) return
        stopping.set(true)
        handler?.removeCallbacksAndMessages(null)
        val current = recorder
        recorder = null
        runCatching { current?.stop() }
        runCatching { current?.release() }
        workingFile().delete()
        recording.set(false)
        stopping.set(false)
    }

    /** 録れた音を共有ストレージへ出す。 */
    private fun publish(working: File) {
        if (!SharedStorage.prepare(SharedStorage.audioDir)) {
            working.delete()
            fail("共有ストレージへ書けない。全ファイルアクセスの許可が要る")
            return
        }

        val name = fileName(startedAt)
        val destination = File(SharedStorage.audioDir, name)
        val temporary = File(SharedStorage.audioDir, "$name.tmp")

        try {
            // キャッシュは /data、置き場は /sdcard で別のマウントなので、
            // renameTo では移せない。中身をコピーする。
            working.inputStream().use { input ->
                temporary.outputStream().use { output ->
                    input.copyTo(output)
                    output.fd.sync()
                }
            }

            // IDF は mtime を RelatedTime にする。録音の開始時刻に合わせておかないと、
            // 取り込んだ日時で記録されてしまう。rename より先に合わせておけば、
            // 現れた瞬間から正しい mtime を持ち、運ばれるのと競合しない。
            if (!temporary.setLastModified(startedAt)) {
                Log.w(TAG, "録音時刻を mtime に反映できなかった: ${temporary.name}")
            }
            if (!temporary.renameTo(destination)) {
                Log.w(TAG, "録った音の名前を変えられなかった: ${temporary.name}")
                temporary.delete()
                return
            }

            lastRecordedAt = startedAt
            lastFailure = ""
            Log.i(TAG, "録音を保存した: ${destination.name} (${destination.length()} bytes)")
        } catch (e: Exception) {
            temporary.delete()
            fail("録った音を置き場へ出せなかった: ${e.message}")
        } finally {
            working.delete()
        }
    }

    private fun fail(reason: String) {
        Log.w(TAG, reason)
        lastFailure = reason
        recording.set(false)
        stopping.set(false)
    }

    /**
     * 録り終えるまでの置き場。区切りごとに別の名前にする。
     *
     * サービスが作り直されると AudioCollector も別物になり、[recording] の
     * 排他が効かない。固定名だと、前のサービスの書き出しの途中で
     * 新しいサービスが同じファイルへ録り始め、壊れたものを出すか、
     * 録れたものを消してしまう。
     */
    private fun workingFile(): File = File(context.cacheDir, "$WORKING_PREFIX$startedAt.m4a")

    /** 例: <端末名>_2026-08-23_12-00-00.m4a （スクリーンショットと同じ形） */
    private fun fileName(at: Long): String {
        val stamp = SimpleDateFormat("yyyy-MM-dd_HH-mm-ss", Locale.US).format(Date(at))
        return "${config.device}_$stamp.m4a"
    }

    companion object {
        private const val TAG = "AutologAudio"
        private const val THREAD_NAME = "AutologAudio"

        /**
         * 最後に録れた時刻。0 なら一度も録れていない。
         *
         * 直近の失敗の理由と合わせて、設定画面の状態表示に出す。
         * 録れているつもりで録れていない、に気づけるようにするため。
         * 設定画面もサービスも同じプロセスにあるのでここに置く。
         */
        @Volatile
        var lastRecordedAt: Long = 0
            private set

        /** 直近の失敗の理由。空なら失敗していない。 */
        @Volatile
        var lastFailure: String = ""
            private set
        private const val MINUTE_MS = 60 * 1000L

        /** 録り終えるまでの置き場の名前。うしろに区切りの時刻が付く。 */
        private const val WORKING_PREFIX = "audio_recording_"

        /**
         * 録音の設定。常時記録なので、聞き取れる範囲で小さくする。
         * モノラル 16kHz 32kbps で1分あたり約 240KB。
         *
         * MPEG_4 コンテナ + AAC-LC は minSdk 26 のどの端末でも使える。
         * 拡張子は .m4a にする。gkill の IDF がこれを音声として扱う。
         */
        private const val CHANNELS = 1
        private const val SAMPLE_RATE_HZ = 16000
        private const val BIT_RATE = 32000
    }
}
