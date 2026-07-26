package com.mt3hr.gkill_autolog.export

import android.content.Context
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/**
 * 定期的に生ログを共有ストレージへ書き出す。
 *
 * 書き出せなくても失敗扱いにはしない。端末に残したまま次回やり直す。
 *
 * 常駐サービス自身も定期的に書き出しているので、これは
 * サービスが動いていないときの保険。
 */
class ExportWorker(context: Context, params: WorkerParameters) : Worker(context, params) {

    override fun doWork(): Result {
        JsonlExporter(applicationContext).export()
        return Result.success()
    }

    companion object {
        private const val WORK_NAME = "gkill_autolog_export"

        /** WorkManager の周期の下限は15分。 */
        private const val INTERVAL_MINUTES = 15L

        fun schedule(context: Context) {
            // 書き出し先は端末内なので、通信の条件は付けない。
            val request = PeriodicWorkRequestBuilder<ExportWorker>(
                INTERVAL_MINUTES, TimeUnit.MINUTES
            ).build()

            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                WORK_NAME, ExistingPeriodicWorkPolicy.UPDATE, request
            )
        }

        fun cancel(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
        }
    }
}
