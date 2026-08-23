package com.mt3hr.gkill_autolog.collect

import android.app.usage.UsageEvents
import android.app.usage.UsageStatsManager
import android.content.Context
import android.content.pm.PackageManager
import com.mt3hr.gkill_autolog.Config
import com.mt3hr.gkill_autolog.model.Event
import com.mt3hr.gkill_autolog.model.EventType
import com.mt3hr.gkill_autolog.store.EventStore
import org.json.JSONObject

/**
 * UsageStatsManager からアプリの利用区間を作る。
 *
 * 前面に来てから離れるまでを1区間とする。分割画面では両方が前面になるため、
 * それぞれ別の区間として記録される。
 *
 * gkill へ出すのは表示上のアプリ名だけで、パッケージ名は生ログにのみ残す（要件 §11.2）。
 * 短い利用 (1分未満) を落とす判定は取り込み時の normalize (MinAppUsage) が行う。
 */
class AppUsageCollector(
    private val context: Context,
    private val store: EventStore,
) {
    private val usageStatsManager: UsageStatsManager? =
        context.getSystemService(Context.USAGE_STATS_SERVICE) as? UsageStatsManager

    private val packageManager: PackageManager = context.packageManager

    /** 前回どこまで読んだか。重複して記録しないために持つ。 */
    private var lastProcessedAt: Long = 0

    /**
     * 直近に Chrome が前面だった区間。
     * 履歴DBの訪問が実際に表示されたものかを判断するのに使う（要件 §12）。
     */
    private val chromeForegroundRanges = mutableListOf<LongRange>()

    /**
     * 前回の続きから現在までのアプリ利用を記録する。
     * サービスから定期的に呼ぶ。
     */
    fun collect(now: Long = System.currentTimeMillis()) {
        val manager = usageStatsManager ?: return

        // 記録するかどうかは区間ごとに見る。ここで打ち切らないのは、
        // 記録しない設定でも Chrome の前面区間だけは覚えておく必要があるため。
        val record = Config(context).collectAppUsage

        val from = if (lastProcessedAt == 0L) now - INITIAL_LOOKBACK_MS else lastProcessedAt
        if (from >= now) return

        val events = manager.queryEvents(from, now)
        val foregroundSince = mutableMapOf<String, Long>()
        val usageEvent = UsageEvents.Event()

        while (events.hasNextEvent()) {
            events.getNextEvent(usageEvent)
            val packageName = usageEvent.packageName ?: continue

            when (usageEvent.eventType) {
                UsageEvents.Event.ACTIVITY_RESUMED ->
                    foregroundSince[packageName] = usageEvent.timeStamp

                UsageEvents.Event.ACTIVITY_PAUSED,
                UsageEvents.Event.ACTIVITY_STOPPED -> {
                    val start = foregroundSince.remove(packageName) ?: continue
                    if (usageEvent.timeStamp <= start) continue

                    if (packageName == PACKAGE_CHROME) {
                        rememberChromeRange(start..usageEvent.timeStamp)
                    }
                    // アプリ利用を記録しない設定でも、Chrome の前面区間は覚えたまま
                    // ここへ来る。この区間が無いと ChromeHistoryCollector が
                    // 「その訪問が実際に表示されたか」を判断できず、閲覧の記録が
                    // 丸ごと落ちてしまう（要件 §12）。
                    if (!record) continue
                    if (isExcluded(packageName)) continue

                    // 前面のままのアプリがあると lastProcessedAt がその開始時刻まで戻るため、
                    // 既に確定した区間を次回も拾ってしまう。
                    // ID を区間そのものから決めることで、送っても受け口が弾いてくれる。
                    val eventId = "app_usage:$packageName:$start:${usageEvent.timeStamp}"

                    store.put(
                        Event.interval(
                            EventType.APP_USAGE, start, usageEvent.timeStamp,
                            JSONObject()
                                .put("app_label", appLabel(packageName))
                                .put("package_name", packageName),
                            eventId = eventId,
                        )
                    )
                }
            }
        }

        // 前面のままのアプリは区間が確定していない。次回に持ち越す。
        lastProcessedAt = foregroundSince.values.minOrNull() ?: now
    }

    /**
     * Chrome が前面だった区間を返す。
     *
     * 履歴DBの訪問のうち、この区間に入っているものだけを閲覧として扱う。
     * 古い区間は保持し続けても意味がないので、上限を超えたら古いものから捨てる。
     */
    fun recentChromeForegroundRanges(): List<LongRange> = chromeForegroundRanges.toList()

    private fun rememberChromeRange(range: LongRange) {
        chromeForegroundRanges.add(range)
        while (chromeForegroundRanges.size > MAX_CHROME_RANGES) {
            chromeForegroundRanges.removeAt(0)
        }
    }

    /** 表示上のアプリ名。取得できなければパッケージ名をそのまま使う。 */
    private fun appLabel(packageName: String): String = try {
        val info = packageManager.getApplicationInfo(packageName, 0)
        packageManager.getApplicationLabel(info).toString()
    } catch (_: PackageManager.NameNotFoundException) {
        packageName
    }

    /**
     * ホーム・ランチャー・設定・通知パネルは除外する（要件 §11.2）。
     */
    private fun isExcluded(packageName: String): Boolean =
        EXCLUDED_PACKAGES.any { packageName == it || packageName.startsWith("$it.") } ||
            packageName == context.packageName ||
            isLauncher(packageName)

    private fun isLauncher(packageName: String): Boolean =
        packageName.contains("launcher", ignoreCase = true) ||
            packageName.contains("systemui", ignoreCase = true)

    companion object {
        /** 初回だけ少し遡る。取りこぼしを減らしつつ、古すぎるものは拾わない。 */
        private const val INITIAL_LOOKBACK_MS = 60L * 60L * 1000L

        const val PACKAGE_CHROME = "com.android.chrome"

        /** 覚えておく Chrome 前面区間の上限。 */
        private const val MAX_CHROME_RANGES = 200

        private val EXCLUDED_PACKAGES = listOf(
            "com.android.settings",
            "com.android.systemui",
            "com.google.android.apps.nexuslauncher",
            "com.android.launcher",
        )
    }
}
