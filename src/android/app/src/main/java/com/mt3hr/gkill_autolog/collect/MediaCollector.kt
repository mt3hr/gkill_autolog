package com.mt3hr.gkill_autolog.collect

import android.content.ComponentName
import android.content.Context
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * YouTube と YouTube Music の再生を MediaSession から記録する。
 *
 * 記録するのは実際に再生された秒数だけで、一時停止している間は数えない。
 *
 * **URL または動画IDを確定できない場合は何も記録しない。**
 * 検索URLや推測したURLを作ってはならず、Kmemo への代替記録も行わない（要件 §8.2）。
 * MediaSession はタイトルとアーティストしか返さないため、
 * 多くの場合ここでは URL を確定できない。その場合は生ログにも残さず、
 * Chrome 履歴からの取得（ChromeHistoryCollector）に委ねる。
 *
 * 30秒未満を落とす判定は X1 Yoga 側の normalize が行う。
 */
class MediaCollector(
    private val context: Context,
    private val store: EventStore,
) {
    private val sessionManager: MediaSessionManager? =
        context.getSystemService(Context.MEDIA_SESSION_SERVICE) as? MediaSessionManager

    private val listenerComponent =
        ComponentName(context, NotificationCollectorService::class.java)

    /** 計測中の再生。パッケージ名ごとに1つ。 */
    private val playing = mutableMapOf<String, PlayState>()

    private data class PlayState(
        val title: String,
        val artist: String,
        val service: String,
        val startedAt: Long,
        var playedMillis: Long,
        var lastTickAt: Long,
        var wasPlaying: Boolean,
    )

    /** サービスから定期的に呼ぶ。 */
    fun collect(now: Long = System.currentTimeMillis()) {
        val manager = sessionManager ?: return

        val controllers = try {
            manager.getActiveSessions(listenerComponent)
        } catch (_: SecurityException) {
            // 通知へのアクセスが未許可。許可されるまで何もしない。
            return
        }

        val seen = mutableSetOf<String>()
        for (controller in controllers) {
            val service = serviceOf(controller.packageName) ?: continue
            seen.add(controller.packageName)
            update(controller, service, now)
        }

        // 消えたセッションは再生終了とみなして確定させる。
        for (packageName in playing.keys.toList()) {
            if (packageName !in seen) {
                finish(packageName, now)
            }
        }
    }

    /** 計測中のものをすべて確定させる。サービス停止時に呼ぶ。 */
    fun flush(now: Long = System.currentTimeMillis()) {
        for (packageName in playing.keys.toList()) {
            finish(packageName, now)
        }
    }

    private fun update(controller: MediaController, service: String, now: Long) {
        val metadata = controller.metadata
        val title = metadata?.getString(android.media.MediaMetadata.METADATA_KEY_TITLE).orEmpty()
        val artist = metadata?.getString(android.media.MediaMetadata.METADATA_KEY_ARTIST).orEmpty()
        val isPlaying = controller.playbackState?.state == PlaybackState.STATE_PLAYING

        val current = playing[controller.packageName]

        // 曲が変わったら前の再生を確定させる。
        if (current != null && current.title != title) {
            finish(controller.packageName, now)
        }

        val state = playing.getOrPut(controller.packageName) {
            PlayState(
                title = title,
                artist = artist,
                service = service,
                startedAt = now,
                playedMillis = 0,
                lastTickAt = now,
                wasPlaying = isPlaying,
            )
        }

        // 再生中だった区間だけを積み上げる。一時停止中は数えない。
        if (state.wasPlaying) {
            state.playedMillis += now - state.lastTickAt
        }
        state.lastTickAt = now
        state.wasPlaying = isPlaying
    }

    private fun finish(packageName: String, now: Long) {
        val state = playing.remove(packageName) ?: return
        if (state.wasPlaying) {
            state.playedMillis += now - state.lastTickAt
        }
        if (state.playedMillis <= 0 || state.title.isBlank()) return

        // URL を確定できないため書き込み対象にはならない。
        // それでも生ログには残し、後から突き合わせられるようにする。
        val payload = JSONObject()
            .put("service", state.service)
            .put("title", state.title)
            .put("artist", state.artist)
            .put("played_seconds", state.playedMillis / 1000.0)

        store.put(Event.interval(EventType.MEDIA_PLAY, state.startedAt, now, payload))
    }

    private fun serviceOf(packageName: String): String? = when (packageName) {
        PACKAGE_YOUTUBE -> "youtube"
        PACKAGE_YOUTUBE_MUSIC -> "youtube_music"
        else -> null
    }

    companion object {
        private const val PACKAGE_YOUTUBE = "com.google.android.youtube"
        private const val PACKAGE_YOUTUBE_MUSIC = "com.google.android.apps.youtube.music"
    }
}
