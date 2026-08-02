package com.mt3hr.gkill_autolog.collect

import android.content.ComponentName
import android.content.Context
import android.content.pm.PackageManager
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * 動画・音楽の再生を MediaSession から記録する。
 *
 * 対象は YouTube と YouTube Music に限らず、MediaSession を持つアプリすべて。
 * 自アプリだけ除外する。
 *
 * 記録するのは実際に再生された秒数だけで、一時停止している間は数えない。
 *
 * **検索URLや推測したURLは作らない**（要件 §8.2）。
 * ただし YouTube 系はメタデータの中に動画IDそのものが入っていることがあるので、
 * [extractVideoId] で確認できたときだけ正規URLを組み立てる。
 * 確認できた動画IDから正規URLを作るのは推測ではない。
 * 他のアプリでは URL を作らない。アートURIがたまたま同じ形をしていても、
 * それが YouTube の動画IDである保証がないため。
 *
 * 動画IDを確認できなかった再生も生ログには残す。
 * タイトルとアーティストは MediaSession から観測できた事実なので、
 * normalize が TimeIs にする（要件 §8.2）。
 *
 * 30秒未満を落とす判定は normalize が行う。
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
        /** 表示上のアプリ名。 */
        val appLabel: String,
        /** 確認できた動画ID。確認できなければ空。 */
        var videoId: String,
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
            if (controller.packageName == context.packageName) continue
            seen.add(controller.packageName)
            update(controller, serviceOf(controller.packageName), now)
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
                appLabel = appLabel(controller.packageName),
                videoId = "",
                startedAt = now,
                playedMillis = 0,
                lastTickAt = now,
                wasPlaying = isPlaying,
            )
        }

        // 動画IDは再生開始直後のメタデータにはまだ入っていないことがある。
        // 一度確認できたらそのまま持ち、以降は上書きしない。
        // YouTube 系以外では取り出さない。別のサービスのアートURIが同じ形をしていても
        // それが YouTube の動画IDだとは限らず、存在しないURLを作ってしまう。
        if (state.videoId.isEmpty() && isYouTubeService(state.service)) {
            state.videoId = extractVideoId(metadata).orEmpty()
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

        val payload = JSONObject()
            .put("service", state.service)
            .put("title", state.title)
            .put("artist", state.artist)
            .put("app_label", state.appLabel)
            .put("played_seconds", state.playedMillis / 1000.0)

        // 動画IDを確認できたときだけ URL を載せる。
        // 載っていれば normalize が TimeIs に加えて URLog も作る。
        if (state.videoId.isNotEmpty()) {
            payload.put("video_id", state.videoId)
            payload.put("url", watchUrl(state.service, state.videoId))
        }

        store.put(Event.interval(EventType.MEDIA_PLAY, state.startedAt, now, payload))
    }

    /** 再生元の種別。YouTube 系以外はすべて app。 */
    private fun serviceOf(packageName: String): String = when (packageName) {
        PACKAGE_YOUTUBE -> SERVICE_YOUTUBE
        PACKAGE_YOUTUBE_MUSIC -> SERVICE_YOUTUBE_MUSIC
        else -> SERVICE_APP
    }

    private fun isYouTubeService(service: String): Boolean =
        service == SERVICE_YOUTUBE || service == SERVICE_YOUTUBE_MUSIC

    /** 表示上のアプリ名。取得できなければパッケージ名をそのまま使う。 */
    private fun appLabel(packageName: String): String = try {
        val packageManager = context.packageManager
        val info = packageManager.getApplicationInfo(packageName, 0)
        packageManager.getApplicationLabel(info).toString()
    } catch (_: PackageManager.NameNotFoundException) {
        packageName
    }

    /** 確認できた動画IDから、そのサービスでの正規URLを組み立てる。 */
    private fun watchUrl(service: String, videoId: String): String {
        val host = if (service == SERVICE_YOUTUBE_MUSIC) "music.youtube.com" else "www.youtube.com"
        return "https://$host/watch?v=$videoId"
    }

    companion object {
        private const val PACKAGE_YOUTUBE = "com.google.android.youtube"
        private const val PACKAGE_YOUTUBE_MUSIC = "com.google.android.apps.youtube.music"

        private const val SERVICE_YOUTUBE = "youtube"
        private const val SERVICE_YOUTUBE_MUSIC = "youtube_music"
        private const val SERVICE_APP = "app"

        /**
         * サムネイルURIに埋まっている動画ID。
         *
         * `https://i.ytimg.com/vi/<動画ID>/hqdefault.jpg` と
         * `https://i.ytimg.com/vi_webp/<動画ID>/hqdefault.webp` の両方を受ける。
         * 動画IDは11文字の URL-safe base64。
         */
        private val THUMBNAIL_VIDEO_ID = Regex("/vi(?:_[a-z]+)?/([A-Za-z0-9_-]{11})/")

        /**
         * メタデータから動画IDを取り出す。確認できなければ null。
         *
         * MediaSession は URL を持たないが、サムネイルURIのパスには動画IDが入っている。
         * `i.ytimg.com/vi/<動画ID>/` の `<動画ID>` は動画IDそのものなので、
         * ここから正規URLを組み立てるのは推測ではない。
         *
         * **`METADATA_KEY_MEDIA_ID` は使わない。** 書式検査を通る11文字であっても
         * 動画IDとは限らず、プレイリスト内の項目IDなど別のものが入りうる。
         * 取り違えると存在しない動画の URL を作り、gkill がそれを取得しに行って
         * エラーページのタイトルを保存してしまう。
         * 誤った URL を残すくらいなら、URL 無しの TimeIs にしたほうがよい。
         *
         * YouTube Music のアルバムアートは googleusercontent.com のことが多く、
         * その場合は動画IDを含まないので取り出せない。それも TimeIs になる。
         */
        fun extractVideoId(metadata: android.media.MediaMetadata?): String? {
            if (metadata == null) return null

            val artUris = listOf(
                android.media.MediaMetadata.METADATA_KEY_DISPLAY_ICON_URI,
                android.media.MediaMetadata.METADATA_KEY_ART_URI,
                android.media.MediaMetadata.METADATA_KEY_ALBUM_ART_URI,
            )
            for (key in artUris) {
                val uri = metadata.getString(key) ?: continue
                val matched = THUMBNAIL_VIDEO_ID.find(uri) ?: continue
                return matched.groupValues[1]
            }
            return null
        }
    }
}
