---
name: autolog-android
description: "Android 収集アプリ（src/android/）と Android 上で動く Go の約束。書き出しファイル名の衝突で rename が黙って上書きすること、GpsPointStore の onUpgrade でテーブルを作り直さないこと、記録種別の既定値を false にしないことと「端末の利用」に切り替えを置かないこと、マイク種別の前景サービスは失敗を飲み込んで残りで入り直すこと、ioExecutor が単一スレッドであること、MediaRecorder は使うスレッドの上で生成すること、共有ストレージに SQLite を置かないこと、Go の time.Local が UTC 固定であることを扱う。src/android/・src/autolog/internal/config/timezone_android.go を編集するとき必読。「更新後に何も記録しなくなった」「当日の GPX が短くなる」の調査でも必読。"
---

# Android 収集アプリの不変条件

対象: `src/android/**` / `src/autolog/internal/config/timezone_android.go`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**
多くは「例外もエラーも出さずに静かに壊れる」種類で、破っても目の前ではエラーにならない。

## Android で踏みやすい落とし穴

- **Go は `time.Local` を UTC に固定する。** 標準ライブラリの Android 実装がそうなっている。`TZ` を設定しても効かない。システム設定から読んで自分で設定する
- **共有ストレージに SQLite を置かない。** FUSE でロックが効かない。JSONL で受け渡す
- **書き出しは排他する。** 常駐サービスと WorkManager が同時に走ると二重に書き出す
- **`su` 経由のシェルは補助グループを持たない。** ネットワークも `/sdcard` も使えない。実際の Termux とは別物
- **マイクは位置情報の真似をしてはいけない。** 「バックグラウンドでのマイク」に当たる
  権限が無いので、マイク種別の前景サービスは背景からは開始できず、再起動からは名指しで
  禁止されている。バッテリー最適化の対象外も免除にならない。位置情報と同じ形で
  `startForeground` の種別に足すと、再起動のたびに失敗して `stopSelf` に落ち、
  **収集が丸ごと止まる。** マイクだけ失敗を飲み込んで残りの種別で入り直す。
  一度掴んだものは、宣言し直せなくても剥がさない
- **常駐の `ioExecutor` は単一スレッド。** screencap・Chrome 履歴・GPX・生ログの
  書き出しが全部載っている。数分かかる処理を載せると収集が丸ごと止まる
- **`MediaRecorder` は使うスレッドの上で生成する。** コールバックは生成したときの
  Looper へ配られる。主スレッドで作ると停止処理まで主スレッドに来る。
  `prepare`/`start` が投げたら `release` する。掴んだままだとマイクが塞がり、
  次の区切りも失敗し続ける
- **`getMaxAmplitude()` の最初の1回は 0 を返す。** 録り始めに一度読んで捨てておかないと、
  停止の直前に読む値がいつも 0 になり、無音の検出が働かない
- **アプリのキャッシュから `/sdcard` へは `renameTo` で移せない。** 別のマウントなので
  失敗する。中身をコピーしてから、`/sdcard` の中で tmp → 最終名の rename をする
- **区切りで丸めた時刻を、途中から始めた記録に名乗らせない。** 収集を始めた時点の
  区切りは頭から観測できていない。そこを 0 に戻して即座に記録すると、12:47 に
  始めたものが「12:00 の記録」として残る。IDF は mtime を記録時刻にするので、
  そのまま gkill へ入る
- **記録をやめる設定にするときは、開いている接続区間を閉じる。** Wi-Fi・Bluetooth・充電。
  切断を書かずに止めると、取り込み側は継続中とみなす。常駐が続く限り観測の切れ目は
  来ないので、区間が何日でも育つ。そのために接続中のものを控えておく

## 書き出しファイル名は既存を避けて採番する

`export/JsonlExporter.kt` の `writeBatch`。

> 名前が既存ファイルと重なると **rename が黙って上書きし、
> 書き出し済み（＝端末からは削除済み）の生ログが失われる。**
> 時計の巻き戻りや同一ミリ秒でも重ならないよう、連番を足したうえで既存の名前を避ける。

`stamp` + `sequence.incrementAndGet()` + `do { } while (destination.exists() || temporary.exists())`
の3点セットが防御。rename の前に `out.fd.sync()` で確実に書き終える。

## GpsPointStore の onUpgrade でテーブルを作り直さない

```kotlin
/**
 * 移行する。**テーブルを作り直してはいけない。**
 *
 * [com.mt3hr.gkill_autolog.export.GpxWriter] はその日の GPX を
 * DB の全点から毎回作り直す。ここで点を消すと、次の書き出しで
 * 書き出し済みの当日分の軌跡が短くなって消えてしまう。
 */
```

列を足すときは `ALTER TABLE ... ADD COLUMN`。移行前の点は精度が分からないので
NULL のままにして「精度不明」として扱う。

## 記録種別の既定値を false にしない／「端末の利用」に切り替えを置かない

`Config.kt` の記録種別。

> 記録する種類。ここから下の6つは既定が true。
>
> **後から足した設定なので、既定を false にすると更新した時点で
> それまで記録できていたものが黙って止まる。**
>
> **端末の利用 (ロック解除・画面消灯・収集の開始と終了) には切り替えを置かない。**
> 取り込み側がこれを使って利用セッションの区間を組み立てており、
> 止めるとアプリ利用も再生も区間として閉じられなくなる
> (normalize の `window.go` の継続中セッション、`state.go` の観測の切れ目)。

## 書き出しはサービス自身が回す。WorkManager は保険

`AutologService.kt` の `exportIfDue`。

> WorkManager の周期タスクは最初の実行まで最大15分待たされるうえ、
> 端末の状態によっては後回しにされる。常駐しているのだから、
> サービス自身が定期的に書き出すほうが確実で速い。
> WorkManager 側はサービスが動いていないときの保険として残してある。

`ExportWorker` の `INTERVAL_MINUTES = 15L` —— **WorkManager の周期の下限は15分**。

## time.Local は UTC 固定。TZ は効かない

`internal/config/timezone_android.go` の `InitLocalTimezone`。

> Go は Android では `time.Local` を UTC に固定する
> (標準ライブラリ `zoneinfo_android.go` の `initLocal` が `localLoc = *UTC` のまま)。
> そのままだと記録した時刻がすべて +00:00 になり、PC 側が書いた +09:00 の行と混ざる。
>
> 生ログも gkill も時刻を文字列で持ち、**その文字列で並べ替える**ため、
> オフセットが混ざると並び順が時刻順と一致しなくなる。
> **絶対時刻としては正しくても、後から直すのが難しい壊れ方をする。**
>
> `time.LoadLocation` 自体は Android でも動く（tzdata を読める）ので、
> どのタイムゾーンかを自分で決めて `time.Local` に入れてやればよい。

## 関連スキル

- [autolog-pipeline](../autolog-pipeline/SKILL.md) — JSONL の受け口（`inbox`）と、開いている区間の持ち越し
- [autolog-build-release](../autolog-build-release/SKILL.md) — APK の署名と versionCode
- [autolog-gkill-api](../autolog-gkill-api/SKILL.md) — IDF が mtime を記録時刻にする話の出どころ
