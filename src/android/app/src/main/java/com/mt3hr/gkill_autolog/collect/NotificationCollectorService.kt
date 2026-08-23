package com.mt3hr.gkill_autolog.collect

import android.app.Notification
import android.content.pm.PackageManager
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * 通知を記録する。
 *
 * Android に表示された通知の内容だけを扱う。Gmail API などへは接続しない（要件 §13）。
 * 常駐通知は除外し、タイトルと本文が両方空のものも記録しない。
 *
 * 同一通知の更新をまとめる処理と、短時間の同内容再通知の集約は
 * 取り込み時の normalize が行う。ここでは届いたものをそのまま残す。
 *
 * このサービスは MediaSessionManager.getActiveSessions を呼ぶためにも必要。
 * 通知へのアクセスが有効なアプリだけが再生中のメディア情報を取得できる。
 */
class NotificationCollectorService : NotificationListenerService() {

    private val store: EventStore by lazy { EventStore(applicationContext) }

    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        val notification = sbn ?: return
        // このサービスはシステムにバインドされたままなので、常駐サービスを
        // 止めても呼ばれ続ける。「収集を停止」の意図に合わせてここでも見る。
        val config = Config(applicationContext)
        if (!config.collectionEnabled) return
        // 通知を記録しない設定にしても MediaCollector は壊れない。
        // getActiveSessions に要るのはシステム設定の「通知へのアクセス」であって、
        // この設定ではないため。
        if (!config.collectNotifications) return
        record(notification)
    }

    private fun record(sbn: StatusBarNotification) {
        val notification = sbn.notification ?: return

        // 常駐通知は除外する。
        if (notification.flags and Notification.FLAG_ONGOING_EVENT != 0) return

        val extras = notification.extras ?: return
        val title = extras.getCharSequence(Notification.EXTRA_TITLE)?.toString().orEmpty()
        val body = (extras.getCharSequence(Notification.EXTRA_BIG_TEXT)
            ?: extras.getCharSequence(Notification.EXTRA_TEXT))?.toString().orEmpty()

        if (title.isBlank() && body.isBlank()) return

        val notificationKey = sbn.key ?: "${sbn.packageName}:${sbn.id}"

        val payload = JSONObject()
            .put("app_label", appLabel(sbn.packageName))
            .put("package_name", sbn.packageName)
            .put("title", title)
            .put("body", body)
            // 同一通知の更新をまとめるためのキー。
            .put("notification_key", notificationKey)
            .put("ongoing", false)

        // 通知の種類を表す値。Android が持っているものをそのまま入れる。
        // 同じアプリでも種類ごとにチャンネルが分かれるので、
        // 「Chrome のダウンロード完了だけ除外する」のような判定ができる。
        // どちらも無いことがあるので、取れたときだけ入れる。
        val channelId = notification.channelId.orEmpty()
        if (channelId.isNotEmpty()) {
            payload.put("channel_id", channelId)
        }
        val category = notification.category.orEmpty()
        if (category.isNotEmpty()) {
            payload.put("category", category)
        }

        // 同じ通知が同じ時刻で再度届いても二重に記録しない。
        // 内容が変わる更新は postTime も変わるので別イベントとして残る。
        val eventId = "notification:$notificationKey:${sbn.postTime}"

        store.put(Event.instant(EventType.NOTIFICATION, sbn.postTime, payload, eventId = eventId))
    }

    private fun appLabel(packageName: String): String = try {
        val info = packageManager.getApplicationInfo(packageName, 0)
        packageManager.getApplicationLabel(info).toString()
    } catch (_: PackageManager.NameNotFoundException) {
        packageName
    }
}
