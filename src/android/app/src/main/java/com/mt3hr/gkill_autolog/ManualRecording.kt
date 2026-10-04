package com.mt3hr.gkill_autolog

// 編集前に読む: .claude/skills/autolog-android/SKILL.md（この領域の不変条件の正本）

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.drawable.Icon
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.widget.Toast
import androidx.core.content.ContextCompat
import com.mt3hr.gkill_autolog.collect.AudioRecorder
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicLong

/**
 * 手動録音。設定画面のボタンと、外部アプリ（Tasker）からの [RecordingReceiver] で始めて止める。
 *
 * 録るのは [AudioRecorder]。ここは「録り続けるかどうか」と、
 * 利用者に見せる通知・トーストを受け持つ。置き場は定期録音と同じで、
 * ファイル名の末尾に `_manual` が付く。
 *
 * **マイクは常駐（[AutologService]）が掴んでいるものを使う。** マイク種別の前景サービスは
 * アプリが前に出ている間しか始められない。背景で届く Tasker の命令から
 * 掴み直すことはできないので、掴めていないときは始めずに理由を出す。
 *
 * **[CHUNK_MINUTES] 分ごとに区切って、別のファイルとして確定させる。**
 * MPEG_4 は止めるときに完成するので、録音中にプロセスが落ちると録りかけは丸ごと使えない。
 * 止め忘れても、失うのは最後の区切りだけにする。区切りの継ぎ目には、
 * 止めて始め直すぶんの1秒に満たない欠けが出る。
 * 各区切りのファイル名と mtime は、その区切りを**実際に録り始めた時刻**にする（丸めない）。
 *
 * 状態の読み書きと各操作は主スレッドから行う（[RecordingReceiver]・画面・常駐はどれも主スレッド）。
 * 録音スレッドから来る結末だけは、主スレッドへ移してから状態を変える。
 */
object ManualRecording {

    /** 誰が操作したか。外部からの操作は設定で受け付けたときだけ通す。 */
    enum class Source {
        /** 設定画面のボタンと、録音中の通知の「録音を停止」。 */
        APP,

        /** 外部アプリ（Tasker）からのブロードキャスト。 */
        EXTERNAL,
    }

    /** 操作の結果。[RecordingReceiver] が `am broadcast` へ返すのに使う。 */
    class Reply(val accepted: Boolean, val message: String)

    /** 1つのファイルにする長さ（分）。これを超えたら区切って次のファイルへ続ける。 */
    const val CHUNK_MINUTES = 60

    /** 録音を続けているか。区切りの継ぎ目でも true のまま。 */
    @Volatile
    var isActive: Boolean = false
        private set

    /** 録音を始めた時刻（最初の区切りの開始）。 */
    @Volatile
    var startedAt: Long = 0
        private set

    /** 直近の失敗の理由。空なら失敗していない。設定画面の状態表示に出す。 */
    @Volatile
    var lastFailure: String = ""
        private set

    /**
     * 録音の通し番号。
     *
     * 止めた直後に始め直すと、前の録音の最後の区切りの結末が、新しい録音の
     * 最中に届く。それを新しい録音のものと取り違えて止めてしまわないために見る。
     */
    private val generation = AtomicLong(0)

    private val mainHandler by lazy { Handler(Looper.getMainLooper()) }

    private val listeners = CopyOnWriteArrayList<() -> Unit>()

    /** 録音の状態が変わったときに主スレッドで呼ぶものを足す。画面が見えている間だけ付ける。 */
    fun addListener(listener: () -> Unit) {
        listeners.addIfAbsent(listener)
    }

    fun removeListener(listener: () -> Unit) {
        listeners.remove(listener)
    }

    /** 録音を始める。主スレッドから呼ぶ。 */
    fun start(context: Context, source: Source): Reply {
        val app = context.applicationContext
        val config = Config(app)

        if (source == Source.EXTERNAL && !config.acceptExternalRecordingControl) {
            return refuse(app, R.string.recording_refused_external)
        }
        if (isActive) return Reply(true, app.getString(R.string.recording_already))
        if (ContextCompat.checkSelfPermission(app, android.Manifest.permission.RECORD_AUDIO) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            return refuse(app, R.string.recording_no_permission)
        }
        if (!AutologService.isMicrophoneForegroundActive()) {
            // 再起動のあと、まだアプリが一度も前に出ていない。ここからは掴めない。
            return refuse(app, R.string.recording_no_microphone)
        }
        val device = config.device
        if (device.isBlank()) return refuse(app, R.string.recording_no_device)

        val now = System.currentTimeMillis()
        val id = generation.incrementAndGet()
        isActive = true
        startedAt = now
        lastFailure = ""

        // 定期録音の途中なら、そこまでを確定させてから始める。同じ録音スレッドに
        // 積むので、定期が止まる前に手動が始まることはない。定期の録れた分は
        // その区切りの時刻で置かれる（実際にその時刻から録っている）。
        AudioRecorder.stop(AudioRecorder.Kind.PERIODIC)
        recordChunk(app, id, device, now)

        showNotification(app, now)
        changed()
        return accept(app, R.string.recording_started)
    }

    /** 録音を止める。録れた分は確定させて置き場へ出す。主スレッドから呼ぶ。 */
    fun stop(context: Context, source: Source): Reply {
        val app = context.applicationContext
        if (source == Source.EXTERNAL && !Config(app).acceptExternalRecordingControl) {
            return refuse(app, R.string.recording_refused_external)
        }
        if (!isActive) return Reply(true, app.getString(R.string.recording_not_active))

        end(app)
        return accept(app, R.string.recording_stopped)
    }

    /** 録音中なら止め、止まっていれば始める。主スレッドから呼ぶ。 */
    fun toggle(context: Context, source: Source): Reply =
        if (isActive) stop(context, source) else start(context, source)

    /**
     * 常駐の都合で録音を打ち切る。マイクを手放したときと、収集を止めたとき。
     *
     * 観測はそこで終わったので、録れた分は捨てずに確定させる。
     */
    fun abandon(context: Context, messageRes: Int) {
        if (!isActive) return
        val app = context.applicationContext
        end(app)
        toast(app, app.getString(messageRes))
    }

    /** 1つの区切りを録る。 */
    private fun recordChunk(app: Context, id: Long, device: String, at: Long) {
        AudioRecorder.start(
            app, AudioRecorder.Kind.MANUAL, device, at, CHUNK_MINUTES * MINUTE_MS,
        ) { result -> onChunkFinished(app, id, device, result) }
    }

    /**
     * 区切りの結末。**録音スレッドから呼ばれる。**
     *
     * 区切りに達して保存できたなら、その場（録音スレッドの上）で次の区切りを始める。
     * 主スレッドを経由すると、その間に定期録音の開始が割り込むことがある。
     */
    private fun onChunkFinished(app: Context, id: Long, device: String, result: AudioRecorder.Result) {
        val ongoing = isActive && generation.get() == id
        if (result.saved && result.timeUp && ongoing) {
            recordChunk(app, id, device, System.currentTimeMillis())
            return
        }

        val reason = when {
            result.busy -> "ほかの録音が続いていて始められなかった"
            result.failure.isNotEmpty() -> result.failure
            // 区切りに達したのに確定できなかった。続けても同じことになる。
            result.timeUp && !result.saved -> "録った音を確定できなかった"
            else -> return
        }
        Log.w(TAG, reason)
        lastFailure = reason
        mainHandler.post {
            // まだこの録音が続いているなら終える（新しい録音を巻き込まない）。
            if (isActive && generation.get() == id) end(app)
            toast(app, app.getString(R.string.recording_failed, reason))
            changed()
        }
    }

    /** 録音を終える。録っている区切りを確定させ、通知を消す。主スレッドから呼ぶ。 */
    private fun end(app: Context) {
        // 先に印を落とす。録音スレッドは区切りの結末でこれを見て、次の区切りへ進まない。
        isActive = false
        AudioRecorder.stop(AudioRecorder.Kind.MANUAL)
        cancelNotification(app)
        changed()
    }

    private fun accept(app: Context, messageRes: Int): Reply {
        val message = app.getString(messageRes)
        toast(app, message)
        return Reply(true, message)
    }

    private fun refuse(app: Context, messageRes: Int): Reply {
        val message = app.getString(messageRes)
        Log.i(TAG, message)
        toast(app, message)
        return Reply(false, message)
    }

    /**
     * 結果をトーストで見せる。Tasker から操作したときは画面に何も出ないので、
     * 始まったのか止まったのか、断られたのかを知る手段がこれしかない。
     * 文字だけのトーストはアプリが背景にいても出せる。
     */
    private fun toast(app: Context, message: String) {
        mainHandler.post { Toast.makeText(app, message, Toast.LENGTH_SHORT).show() }
    }

    private fun changed() {
        mainHandler.post { listeners.forEach { it() } }
    }

    /**
     * 録音中であることを通知に出す。
     *
     * 外部から始められる以上、録っていることに気づけないといけない。
     * 止め忘れたときに、通知から止められるようにもする。
     * 通知の許可が無いと出ない（トーストは出る）。
     */
    private fun showNotification(app: Context, since: Long) {
        val manager = app.getSystemService(NotificationManager::class.java) ?: return
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                app.getString(R.string.recording_channel_name),
                // 音は鳴らさないが、ステータスバーには出す。
                NotificationManager.IMPORTANCE_LOW,
            )
        )

        val openApp = PendingIntent.getActivity(
            app, 0, Intent(app, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getBroadcast(
            app, 0, Intent(app, RecordingStopReceiver::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val clock = SimpleDateFormat("HH:mm", Locale.US).format(Date(since))

        val notification = Notification.Builder(app, CHANNEL_ID)
            .setContentTitle(app.getString(R.string.recording_notification_title))
            .setContentText(app.getString(R.string.recording_notification_text, clock, CHUNK_MINUTES))
            .setSmallIcon(android.R.drawable.ic_btn_speak_now)
            // 経過時間を出す。
            .setWhen(since)
            .setShowWhen(true)
            .setUsesChronometer(true)
            .setContentIntent(openApp)
            .setOngoing(true)
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource("android", android.R.drawable.ic_media_pause),
                    app.getString(R.string.action_stop_recording),
                    stop,
                ).build()
            )
            .build()

        runCatching { manager.notify(NOTIFICATION_ID, notification) }
            .onFailure { Log.w(TAG, "録音中の通知を出せなかった: ${it.message}") }
    }

    private fun cancelNotification(app: Context) {
        app.getSystemService(NotificationManager::class.java)?.cancel(NOTIFICATION_ID)
    }

    private const val TAG = "AutologRecording"
    private const val CHANNEL_ID = "gkill_autolog_recording"

    /** 常駐の通知（AutologService の NOTIFICATION_ID = 1）と重ねない。 */
    private const val NOTIFICATION_ID = 2

    private const val MINUTE_MS = 60 * 1000L
}
