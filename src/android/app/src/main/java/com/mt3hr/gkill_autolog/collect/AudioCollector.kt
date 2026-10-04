package com.mt3hr.gkill_autolog.collect

import android.content.Context
import android.util.Log
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.ManualRecording

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
 * 手動録音がマイクを使っている間に来た区切りも、同じように空ける。
 *
 * ここは区切りを決めるだけで、録音そのものは [AudioRecorder] が行う
 * （手動録音とマイクを取り合わないため。録音スレッドもあちらが持つ）。
 */
class AudioCollector(
    private val context: Context,
    private val config: Config,
) {
    private val screen = ScreenState(context)

    /** 直近に処理した区切り。同じ区切りで二度録らないために持つ。 */
    private var lastBucket: Long = 0

    /** 直近に使った録音間隔。設定が変わったことに気づくために持つ。 */
    private var lastIntervalMs: Long = 0

    /**
     * 録音の時刻を過ぎていれば録り始める。サービスから定期的に呼ぶ。
     *
     * 区切りの判定だけをこの場で行い、録音そのものは録音スレッドへ渡す。
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
        if (ManualRecording.isActive) {
            // 手動録音がマイクを使っている。後から録り直さないので、この区切りは空ける。
            Log.i(TAG, "手動録音中のため、この区切りは録らない")
            return
        }
        if (AudioRecorder.currentKind != null) {
            // 前の録音がまだ終わっていない。長さが間隔を超えているときに起こる。
            Log.w(TAG, "前の録音が続いているため、この区切りは録らない")
            return
        }

        val durationMs = Config
            .clampAudioDurationMinutes(config.audioDurationMinutes, config.audioIntervalMinutes)
            .toLong() * MINUTE_MS

        AudioRecorder.start(
            context, AudioRecorder.Kind.PERIODIC, config.device, bucket, durationMs, ::onFinished,
        )
    }

    /**
     * 定期録音を止めて、録れた分を確定させる。手動録音は巻き込まない。
     *
     * 主スレッド（サービスの onDestroy）から呼ばれる。停止は録音スレッドへ積むだけで、
     * ここでは待たない（待つと ANR になる）。
     */
    fun stop() {
        AudioRecorder.stop(AudioRecorder.Kind.PERIODIC)
    }

    /** 録音の結末を設定画面の状態表示へ反映する。録音スレッドから呼ばれる。 */
    private fun onFinished(result: AudioRecorder.Result) {
        when {
            // 手動録音と入れ違いになった。区切りは空ける。
            result.busy -> Log.i(TAG, "ほかの録音が続いているため、この区切りは録らない")
            result.saved -> {
                lastRecordedAt = result.startedAt
                lastFailure = ""
            }
            result.failure.isNotEmpty() -> lastFailure = result.failure
        }
    }

    companion object {
        private const val TAG = "AutologAudio"

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
    }
}
