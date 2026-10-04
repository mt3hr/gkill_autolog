package com.mt3hr.gkill_autolog.collect

// 編集前に読む: .claude/skills/autolog-android/SKILL.md（この領域の不変条件の正本）

import android.content.Context
import android.media.MediaRecorder
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.util.Log
import com.mt3hr.gkill_autolog.SharedStorage
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * アプリでただ1つの、マイクの持ち主。
 *
 * 定期録音（[AudioCollector]）と手動録音（[com.mt3hr.gkill_autolog.ManualRecording]）は
 * どちらもここを通して録る。**MediaRecorder を2つ同時に開いてマイクを取り合わない**ため。
 * 同じアプリから2本同時に録れるかは端末と OS の版で違い、片方が無音になったり
 * 開始に失敗したりする。どちらに転んでも、録れたつもりの音が残らない。
 *
 * 録音スレッドを1本だけ持ち、開始・停止・時間切れ・書き出しを全部その上で順に行う。
 * 「定期を止めてから手動を始める」が、積んだ順に必ず起きるのはこのため。
 *
 * プロセスで1つにしてあるのは、常駐が作り直されても排他を効かせるため。
 * 録音スレッドは畳まない。待っているだけのスレッドは電池を使わない。
 *
 * **常駐サービスの ioExecutor は使わない。** あちらは単一スレッドで、
 * スクリーンショット・Chrome 履歴・GPX・生ログの書き出しが全部載っている。
 * 数分の録音でそこを塞ぐと、収集が丸ごと止まる。
 *
 * 録った音は共有ストレージへ置くだけ。そこから先へ運ぶのは
 * 同期スクリプト (gkill_server dvnf) の役目で、AutoAudio_<端末>_<日付> にまとめられる。
 */
object AudioRecorder {

    /** 何の録音か。ファイル名の末尾の目印になる。 */
    enum class Kind(val suffix: String) {
        /** 定期録音。<端末名>_<区切りの時刻>.m4a */
        PERIODIC(""),

        /** 手動録音。<端末名>_<録り始めた時刻>_manual.m4a */
        MANUAL("_manual"),
    }

    /** 1回の録音の結末。録音スレッドの上で渡す。 */
    class Result(
        val kind: Kind,
        val startedAt: Long,
        /** 置き場へ出せたか。 */
        val saved: Boolean,
        /** 決めた長さに達して終わったか。手動録音はこれを見て次の区切りへ進む。 */
        val timeUp: Boolean,
        /** ほかの録音が続いていたので始めなかったか。 */
        val busy: Boolean = false,
        /** 利用者に見せる失敗の理由。失敗でなければ空。 */
        val failure: String = "",
    )

    /**
     * いま録っている種類。録っていなければ null。
     *
     * 書くのは録音スレッドだけ。主スレッドからも読むので @Volatile を付ける。
     */
    @Volatile
    var currentKind: Kind? = null
        private set

    /** 1回の録音。録音スレッドだけが触る。 */
    private class Session(
        val kind: Kind,
        val device: String,
        val startedAt: Long,
        val working: File,
        val onFinished: (Result) -> Unit,
    ) {
        var recorder: MediaRecorder? = null

        /** 自前のタイマー。止めるときに、これだけを取り消す。 */
        var timer: Runnable? = null
    }

    /** いま録っている録音。録音スレッドだけが触る。 */
    private var current: Session? = null

    @Volatile
    private var handler: Handler? = null

    private val handlerLock = Any()

    /**
     * 録り始める。録音は録音スレッドで行うので、どのスレッドから呼んでもよい。
     *
     * 録音スレッドの上（[Result] を受けたところ）から呼んだときは、その場で始める。
     * 手動録音が区切りから次の区切りへ続けるとき、間に別の処理を挟ませないため。
     *
     * 結末は [onFinished] へ、録音スレッドの上で渡す。
     */
    fun start(
        context: Context,
        kind: Kind,
        device: String,
        startedAt: Long,
        durationMs: Long,
        onFinished: (Result) -> Unit,
    ) {
        val app = context.applicationContext
        val target = handler(app)
        val begin = Runnable { begin(app, kind, device, startedAt, durationMs, onFinished) }
        if (Looper.myLooper() === target.looper) begin.run() else target.post(begin)
    }

    /**
     * 録音を止めて、録れた分を確定させる。[kind] を渡したときはその種類の録音だけ。
     *
     * 主スレッド（サービスの onDestroy など）から呼ばれる。ここで待つと ANR になるので、
     * 停止は録音スレッドへ積むだけにして戻る。
     */
    fun stop(kind: Kind? = null) {
        // 一度も録っていなければ止めるものも無い。スレッドを立ち上げない。
        val target = handler ?: return
        target.post {
            val session = current ?: return@post
            if (kind == null || session.kind == kind) finish(session, timeUp = false)
        }
    }

    /** 録音スレッド。無ければ立ち上げる。 */
    private fun handler(context: Context): Handler {
        handler?.let { return it }
        synchronized(handlerLock) {
            handler?.let { return it }
            val thread = HandlerThread(THREAD_NAME).apply { start() }
            val created = Handler(thread.looper)
            // 最初に、前のプロセスの録りかけを片付ける。
            created.post { removeLeftovers(context) }
            handler = created
            return created
        }
    }

    /**
     * 前のプロセスが録っている途中で落ちたときの残りを消す。
     *
     * MPEG_4 は止めるときに完成するので、止められなかった録りかけは再生できない。
     * 残すとキャッシュを食い続ける（手動録音なら1つで十数 MB になる）。
     * このプロセスで最初に録音スレッドを立てた時点では、まだ何も録っていない。
     */
    private fun removeLeftovers(context: Context) {
        context.cacheDir.listFiles { file -> file.name.startsWith(WORKING_PREFIX) }
            ?.forEach { file ->
                Log.i(TAG, "前の録りかけを消す: ${file.name}")
                file.delete()
            }
    }

    /**
     * 録り始める。**必ず録音スレッドから呼ぶこと。**
     *
     * MediaRecorder は**作ったときの Looper** へコールバックを配る。
     * 主スレッドで作ると、時間切れの通知も stop も主スレッドに来てしまう。
     */
    private fun begin(
        context: Context,
        kind: Kind,
        device: String,
        startedAt: Long,
        durationMs: Long,
        onFinished: (Result) -> Unit,
    ) {
        if (current != null) {
            onFinished(Result(kind, startedAt, saved = false, timeUp = false, busy = true))
            return
        }

        if (device.isBlank()) {
            // ファイル名が <端末名>_<時刻>.m4a なので、端末名が無いと後で判別できない。
            fail(kind, startedAt, onFinished, "端末名が決まっていない。アプリの設定で端末名を入れること")
            return
        }

        // 録るのはアプリのキャッシュ。共有ストレージへ直に録らないのは2つ理由がある。
        // MPEG_4 は停止するときに moov atom を先頭へ書き戻すのでシークできる先が要ること、
        // 録音中ずっと育ちかけのファイルが置き場に見えて、同期スクリプトに
        // 録りかけを掴まれること。
        //
        // 名前は録音ごとに変える。消し損ねた前の録りかけと混ざらないようにするため。
        val working = File(
            context.cacheDir,
            "$WORKING_PREFIX${kind.name.lowercase(Locale.US)}_$startedAt.m4a",
        )
        working.delete()

        // prepare や start が投げたときに release できるよう、try の外で持つ。
        // 掴んだままにすると、マイクが塞がって次の録音も失敗し続ける。
        @Suppress("DEPRECATION")
        val target = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            MediaRecorder(context)
        } else {
            MediaRecorder()
        }
        val session = Session(kind, device, startedAt, working, onFinished)

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
                if (what == MediaRecorder.MEDIA_RECORDER_INFO_MAX_DURATION_REACHED) {
                    finish(session, timeUp = true)
                }
            }
            target.setOnErrorListener { _, what, extra ->
                // 録れた分は当てにならないので出さない。
                abort(session, "録音中にエラーが起きた ($what, $extra)")
            }
            target.setOutputFile(working.absolutePath)
            target.prepare()
            target.start()
        } catch (e: Exception) {
            // 他のアプリがマイクを使っている、通話中、権限が無い、など。
            // 記録は残さない。定期録音は次の区切りでやり直す。
            runCatching { target.release() }
            working.delete()
            fail(kind, startedAt, onFinished, "録音を始められなかった: ${e.message}")
            return
        }

        // 振幅の記録を空にしておく。getMaxAmplitude は前の呼び出しからの
        // 最大値を返すもので、**最初の1回は 0 を返す。** ここで捨てておかないと、
        // 停止の直前に読む値がいつも 0 になり、無音の検出が働かない。
        runCatching { target.getMaxAmplitude() }

        session.recorder = target
        current = session
        currentKind = kind

        val timer = Runnable { finish(session, timeUp = true) }
        session.timer = timer
        handler?.postDelayed(timer, durationMs)
        Log.i(TAG, "録音を始めた (${kind.name}, ${durationMs / MINUTE_MS} 分)")
    }

    /**
     * 録り終えて、共有ストレージへ出す。
     *
     * 時間切れの通知と自前のタイマー、停止の指示のどれからでも来るので、1回に畳む。
     * 別の録音に移ったあとで古い知らせが来ても、[current] と違えば何もしない。
     */
    private fun finish(session: Session, timeUp: Boolean) {
        if (current !== session) return
        current = null
        currentKind = null

        // 残っているタイマー（保険と本命のうち、来なかったほう）を落とす。
        // 全部を消してはいけない。同じスレッドに積まれた次の録音の開始まで消える。
        session.timer?.let { handler?.removeCallbacks(it) }

        val recorder = session.recorder

        // 停止の直前に一度だけ読む。前の呼び出しからの最大値を返すので、
        // 1回で録音の全体をカバーする。
        val amplitude = runCatching { recorder?.getMaxAmplitude() ?: 0 }.getOrDefault(0)

        val stopped = try {
            recorder?.stop()
            true
        } catch (e: Exception) {
            // 録音が極端に短いとここへ来る。このとき出力は壊れている。
            Log.w(TAG, "録音を止められなかった。この分は捨てる: ${e.message}")
            false
        }
        runCatching { recorder?.release() }

        if (!stopped || session.working.length() == 0L) {
            session.working.delete()
            session.onFinished(Result(session.kind, session.startedAt, saved = false, timeUp = timeUp))
            return
        }

        if (amplitude == 0) {
            // 静かな部屋も、塞がれたマイクも、同じ無音になる。区別できないので、
            // 気づけるように残すだけにして、ファイルは捨てない。
            Log.w(TAG, "録音の全体が無音だった。マイクが塞がれている可能性がある")
        }

        val failure = publish(session)
        if (failure.isNotEmpty()) Log.w(TAG, failure)
        session.onFinished(
            Result(
                session.kind, session.startedAt,
                saved = failure.isEmpty(), timeUp = timeUp, failure = failure,
            )
        )
    }

    /** エラーで打ち切る。録れた分は当てにならないので出さない。 */
    private fun abort(session: Session, reason: String) {
        if (current !== session) return
        current = null
        currentKind = null
        session.timer?.let { handler?.removeCallbacks(it) }

        runCatching { session.recorder?.stop() }
        runCatching { session.recorder?.release() }
        session.working.delete()

        Log.w(TAG, reason)
        session.onFinished(
            Result(session.kind, session.startedAt, saved = false, timeUp = false, failure = reason)
        )
    }

    private fun fail(kind: Kind, startedAt: Long, onFinished: (Result) -> Unit, reason: String) {
        Log.w(TAG, reason)
        onFinished(Result(kind, startedAt, saved = false, timeUp = false, failure = reason))
    }

    /**
     * 録れた音を共有ストレージへ出す。出せなかった理由を返す（出せたら空）。
     */
    private fun publish(session: Session): String {
        val working = session.working
        try {
            if (!SharedStorage.prepare(SharedStorage.audioDir)) {
                return "共有ストレージへ書けない。全ファイルアクセスの許可が要る"
            }

            val (destination, temporary) = destinationFor(session)
            return try {
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
                if (!temporary.setLastModified(session.startedAt)) {
                    Log.w(TAG, "録音時刻を mtime に反映できなかった: ${temporary.name}")
                }
                if (!temporary.renameTo(destination)) {
                    temporary.delete()
                    return "録った音の名前を変えられなかった: ${temporary.name}"
                }

                Log.i(TAG, "録音を保存した: ${destination.name} (${destination.length()} bytes)")
                ""
            } catch (e: Exception) {
                temporary.delete()
                "録った音を置き場へ出せなかった: ${e.message}"
            }
        } finally {
            working.delete()
        }
    }

    /**
     * 置き場での名前（最終名と一時名）を決める。**既にある名前は避ける。**
     *
     * renameTo は行き先が既にあると**黙って上書きする。** 手動録音は同じ秒のうちに
     * 止めて始め直せるし、時計が巻き戻れば定期録音の区切りの時刻も重なる。
     * 重なった先がまだ同期スクリプトに運ばれていない録音なら、それが消える。
     * 生ログの書き出し（JsonlExporter.writeBatch）と同じく、既存の名前を避けて採番する。
     *
     * 例: <端末名>_2026-08-23_12-00-00.m4a、重なったら <端末名>_2026-08-23_12-00-00-2.m4a
     */
    private fun destinationFor(session: Session): Pair<File, File> {
        val stamp = SimpleDateFormat("yyyy-MM-dd_HH-mm-ss", Locale.US).format(Date(session.startedAt))
        val base = "${session.device}_$stamp${session.kind.suffix}"
        var number = 1
        var destination: File
        var temporary: File
        do {
            val name = if (number == 1) "$base.m4a" else "$base-$number.m4a"
            destination = File(SharedStorage.audioDir, name)
            temporary = File(SharedStorage.audioDir, "$name.tmp")
            number++
        } while (destination.exists() || temporary.exists())
        return destination to temporary
    }

    private const val TAG = "AutologAudio"
    private const val THREAD_NAME = "AutologAudio"
    private const val MINUTE_MS = 60 * 1000L

    /** 録り終えるまでの置き場の名前。うしろに種類と開始時刻が付く。 */
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
