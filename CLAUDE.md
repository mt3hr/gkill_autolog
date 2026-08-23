# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

gkill_autolog は、PC・Android 端末・Chrome から客観的な操作ログを自動収集し、
[gkill](https://github.com/mt3hr/gkill) へ Kyou として記録するツール群。

**各端末が自分で集めて自分の gkill へ取り込む。** 端末をまたぐ転送はしない。
その先の集約は gkill 側の既存の同期に任せる。

Go の CLI（収集・取り込み）、Android の収集アプリ、Chrome 拡張、
PowerShell スクリプトで構成される。MIT ライセンス。

## Build & Development Commands

| コマンド | 用途 |
| --- | --- |
| `npm run build` | Windows 向けにビルド（`release/windows_amd64/autolog.exe` を出す） |
| `npm run build_android_arm64` | Android (arm64) 向けにビルド |
| `npm run build_android_apk` | 収集アプリの APK を作る |
| `npm run release` | 全プラットフォーム向けにビルドして成果物を検査 |
| `cd src\autolog && go test ./...` | Go のテスト |
| `npm run test_chrome_ext` | Chrome 拡張のテスト（Node 標準ランナー + chrome スタブ） |
| `cd src\android && .\gradlew.bat --% assembleDebug -PversionName=X -PversionCode=N` | APK |
| `.\src\scripts\check_connection.ps1` | gkill へ繋がるか確認（書き込まない） |
| `.\src\scripts\run_import.ps1 -DryRun -UntilNow` | 取り込みの内容確認 |

**前提:** Go 1.26 以上、JDK 17 + Android SDK（APK を作る場合）。
CGO は使わない（SQLite は純 Go）。

## Architecture

### ディレクトリ

```
src/
  autolog/     Go の CLI。go.mod はここ（module: github.com/mt3hr/gkill_autolog/src/autolog）
  android/     Android 収集アプリ (com.mt3hr.gkill_autolog)
  chrome_ext/  Chrome 拡張 (Manifest V3)
  scripts/     PowerShell スクリプト
  tools/       ビルドの小物（Node。npm スクリプトから呼ばれる）
documents/
  reverse/     設計資料
```

gkill 本体と同じく、実装は `src/` の下、資料は `documents/reverse/` に置く。

### データの流れ

```
収集 → raw.db（追記専用）→ normalize（提案）→ gkill HTTP API
                                                    ↓
                                               ledger.db（書き込み済み）
```

スクリーンショットと GPX（位置情報）は別経路。raw.db と import を通らず、
撮って（書いて）置くだけ。gkill へ入れるのは同期スクリプトと
`gkill_server idf` / gpslog rep の役目。

### サブコマンド

`collect`（常駐収集）/ `import`（取り込み）/ `screenshot`（1枚撮る）/ `status`（状況表示）

### パッケージ

`rawlog`（生ログ）/ `collect`（Windows 収集）/ `winapi` / `shot` /
`ingest`（Chrome の受け口）/ `inbox`（Android の受け口）/
`normalize`（**中核**）/ `gkillclient` / `ledger` / `config` /
`proclock`（多重起動防止のファイルロック）

## 設計上の約束

詳細は `documents/reverse/design-philosophy.md`。

- **観測できた事実だけを記録する。** 目的・感情・集中状態を推測しない。要約しない
- **判断は決定的なルールで行う。** LLM に取捨を任せない。記録した事実のうち何を
  gkill へ出さないかの除外は `url_denylist.txt` と `notification_denylist.txt` のみ
  （要件で決まっている固定の取捨、たとえばブラウザ内部ページや YouTube 系の閲覧の
  除外は normalize のコードにある）。何を観測するか自体の取捨は別で、
  Android は設定画面のチェックボックスで種類ごとに選べる
- **生ログは消さない。** 追記専用。`(端末, event_id)` で一意なので再取り込みが安全
- **失敗は記録せず次回やり直す。** 台帳には成功した分だけ載せる
- **端末名・利用者名をコードに書かない。** 特定環境の決め打ちを置かない。設定で決める
  （既定値は「ホスト名・機種名から導く」のような環境非依存の導出だけ）
- **エラーメッセージは利用者に見える経路（config・cmd・gkillclient・inbox）は日本語。**
  内部診断（winapi・rawlog の低層など）は英語のままでよい

## 踏みやすい落とし穴

これらはすべて実際に起きた問題。

### gkill 側

- **応答は HTTP 200 でも失敗のことがある。** 本文の `errors` を必ず見る
- **`rep_name` は無視される。** 追加ユースケースが書き込み用リポジトリ固定。書き分けは端末別ユーザーで行う
- **ログインは IP ごとに 15 分で 10 回まで。** ユーザー単位ではない。確認スクリプトの連打で本番の取り込みが全滅する。一度失敗したら実行中は再試行しない
- **`add_urlog` はサーバが対象 URL を取得しに行く。** 1秒に1件へ抑え、タイトルは自分で埋める
- **`update_user_reps` は全件置換。** 手で叩かない
- **IDF は mtime を記録時刻にする。** 保存後に撮影時刻へ合わせる

### PowerShell（5.1 と 7 の差）

**ユーザーの実行環境は Windows PowerShell 5.1。** tool の PowerShell は 7 なので再現しない。
切り分けは `powershell.exe -NoProfile -File ...` で行う。

- **BOM なし UTF-8 は行が消える。** 5.1 が Shift_JIS として読み、日本語コメントの2バイト目が改行を飲み込んで次の行を巻き込む。`.ps1` も設定ファイルも BOM 付きで保存する
- **`-File` 起動では param ブロックの `$PSScriptRoot` が空。** 既定値は本体で解決する
- **`2>&1` した native コマンドの stderr が終了エラーになる。** 呼び出しの間だけ `$ErrorActionPreference='Continue'` にし、終了コードで判定する
- **子プロセスの UTF-8 出力が化ける。** 呼び出しの間だけ `[Console]::OutputEncoding` を UTF-8 にする

### Android

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

### Windows 収集

- **Wi-Fi の SSID には位置情報の許可が要る。** 無いと `ERROR_ACCESS_DENIED` で空になる
- **Bluetooth の接続判定は `fConnected` では不正確。** SetupAPI を使う

## テスト

`internal/normalize` が最重要。閾値・結合・持ち越しの境界値を検証している。

**本番の gkill に対して試さない。** 別のホームと別のポートで gkill を起動して検証する
（`documents/reverse/testing-guide.md`）。

## Language

コード内のコメント、コミットメッセージ、ドキュメントは日本語。

Go の書き方は gkill 本体に合わせる。`slices.SortFunc`（`sort.Slice` は使わない）、
`for range n`、`any`（`interface{}` は使わない）、複数エラーは `errors.Join`。
