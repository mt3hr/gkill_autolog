# フォルダ構成

```
gkill_autolog/
├── README.md                概要と最短の導入手順
├── LICENSE                  MIT
├── CLAUDE.md                Claude Code 向けの案内
├── package.json             ビルドの入口（npm スクリプト）
├── documents/
│   └── reverse/             設計資料（このディレクトリ）
└── src/
    ├── README.md            実装の入口
    ├── autolog/             Go の CLI（収集・取り込み）
    ├── android/             Android 収集アプリ
    ├── chrome_ext/          Chrome 拡張
    ├── scripts/             PowerShell スクリプト（設定・取り込み）
    └── tools/               ビルドの小物（Node）
```

gkill 本体と同じく、ビルドは `package.json` の npm スクリプトから行います。
**Node が要るのはビルドのときだけ**で、動かすのに Node は要りません。

gkill 本体と同じく、実装は `src/` の下に、資料は `documents/reverse/` に置きます。

## src/autolog — Go の CLI

モジュールは `github.com/mt3hr/gkill_autolog/src/autolog`。
`go.mod` はこのディレクトリにあります（gkill 本体が `src/server` に置くのと同じ形）。

CGO は使いません。SQLite は純 Go の実装 (`modernc.org/sqlite`) です。
そのため Android への クロスコンパイルが `CGO_ENABLED=0` だけで通ります。

```
autolog/
├── cmd/autolog/          サブコマンド
│   ├── main.go             エントリポイント。タイムゾーンをここで確定させる
│   ├── cmd_collect.go      collect: 常駐して集める
│   ├── cmd_import.go       import: 整理して gkill へ取り込む
│   ├── cmd_screenshot.go   screenshot: 1枚撮る
│   ├── cmd_status.go       status: 溜まり具合を見る
│   └── helpers.go          サブコマンド間で共有する小物
├── internal/
│   ├── rawlog/           生ログ。追記専用 SQLite とイベントの型
│   ├── collect/          PC での収集（ウィンドウ・セッション・Wi-Fi 等）
│   ├── winapi/           Win32 API のラッパ
│   ├── linuxapi/         Linux の収集（D-Bus・evdev・X11）
│   ├── shot/             スクリーンショットの撮影と WebP 変換
│   ├── ingest/           Chrome の受け口 (HTTP)。拡張から閲覧・再生を受け取る
│   ├── inbox/            Android の受け口。収集アプリが置いた JSONL の取り込み
│   ├── normalize/        生ログ → 提案。ルール処理の中核
│   ├── gkillclient/      gkill の HTTP API クライアント。書き込みの制御もここ
│   ├── ledger/           書き込み済み台帳
│   ├── proclock/         多重起動を防ぐ OS ファイルロック
│   └── config/           設定とディレクトリの解決
└── schema/
    └── event.schema.json 生ログのスキーマ
```

### プラットフォーム別のファイル

収集のロジックはビルドタグの無いファイルに置き、OS を叩く部分だけを分けています。
`collect` は `platform_windows.go` / `platform_linux.go` / `platform_other.go`、
`shot` は `capture_windows.go` / `capture_linux.go` / `capture_other.go` です。
どちらも対応していないプラットフォームでは「収集しない」「撮らない」実装になります。

**ビルドタグは `linux && !android` と `!windows && (!linux || android)` で書きます。**
Go では `GOOS=android` が `linux` のビルドタグも満たすため、`linux` だけで分けると
Android 向けビルドが Linux の収集を取り込み、Termux の `autolog` が収集を始めてしまいます
（収集は収集アプリの役目）。`npm run vet_android` がこの抜けを検出します。

`linuxapi` は解析・計算だけの純粋な関数にビルドタグを付けません。
`GOOS=linux` のテストはクロスコンパイルでは実行できないため、タグを付けると
Windows の開発機で一度も走らないコードになります。

タイムゾーンの扱いも同じ形です (`timezone_android.go` / `timezone_other.go`)。

### 依存の向き

```
cmd/autolog
    ├→ collect ─→ winapi
    ├→ shot
    ├→ ingest ──┐
    ├→ inbox ───┼→ rawlog
    ├→ normalize┘
    ├→ gkillclient ─→ ledger
    ├→ proclock
    └→ config ──→ rawlog
```

`normalize` は生ログの型以外に依存しません。純粋なルール処理なので、
入力を並べれば結果が決まり、単体テストが書きやすくなっています。

## src/android — 収集アプリ

パッケージは `com.mt3hr.gkill_autolog`。集めて共有ストレージへ書き出すだけで、
gkill への取り込みはしません。

```
android/app/src/main/java/com/mt3hr/gkill_autolog/
├── AutologApp.kt          Application。落ちたときの記録を crash.log に残す
├── AutologService.kt      常駐して各収集を回す
├── MainActivity.kt        設定と権限付与の画面
├── BootReceiver.kt        再起動後の再開
├── ExportReceiver.kt      外から書き出しをさせる受け口（取り込みの直前に叩かれる）
├── Config.kt              設定。端末名は config.env を優先する
├── SharedStorage.kt       /sdcard/gkill_autolog の場所
├── collect/               各収集（アプリ利用・通知・メディア・システム・撮影・位置情報）
├── export/                JSONL と GPX の書き出し
├── model/Event.kt         生ログの1件
└── store/                 書き出すまでの一時保管（イベント・位置情報）
```

画面まわりのリソースは `app/src/main/res/` にあります
（`layout/activity_main.xml`、`values/strings.xml`・`colors.xml`、
ユーザー補助サービスの宣言 `xml/accessibility_service_config.xml`、アイコン）。

## src/chrome_ext — Chrome 拡張

Manifest V3。閲覧したページと、動画・音楽の実再生時間を送ります。

```
chrome_ext/
├── manifest.json         権限と読み込むファイル
├── background.js         Service Worker。閲覧区間の管理と送信
├── content_media.js      各ページで再生を数える
├── shared.js             Service Worker と設定画面が共有する定数
├── options.html          送信先と共有トークンの設定画面
├── options.js
├── README.md             拡張の設計と落とし穴
├── background.test.mjs   Service Worker のテスト
├── content_media.test.mjs            計測のテスト
└── content_media_insecure.test.mjs   保護されていないページでの計測のテスト
```

テストは Node の標準ランナーで動かします (`npm run test_chrome_ext`)。
chrome API は `chrome.storage` の非同期性を再現したスタブに差し替えます。

Service Worker は随時停止するので、イベントはいったん `chrome.storage` へ積み、
`chrome.alarms` でまとめて送ります。送れた分だけ消します。
積める上限は5000件で、あふれたら古いものから捨てます。

## src/scripts — 運用スクリプト

| スクリプト | 用途 |
| --- | --- |
| `_gkill_api.ps1` | 共通処理。gkill の API 呼び出し、設定ファイルの読み書き、パスの解決 |
| `setup_auto_users.ps1` | 端末別ユーザーを作る |
| `set_auto_password.ps1` | 端末別ユーザー共通のパスワードを設定する |
| `set_password.ps1` | 単一ユーザー構成のパスワードを設定する |
| `check_connection.ps1` | gkill へ繋がるか確かめる（書き込まない） |
| `run_collect.ps1` | autolog.env を読み込んで常駐収集を起動する。タスクスケジューラから呼ぶ |
| `run_import.ps1` | 取り込みを実行する。同期スクリプトから呼ぶ |
| `register_tasks.ps1` | タスクスケジューラへ登録する |
| `collect_android_diag.sh` | Android で撮影・取り込みが動かないときの情報を集める |
| `autolog.env.example` | 設定ファイルの雛形 |
| `linux/_autolog.sh` | Linux 側の共通処理。設定の読み込みと実行体の探索 |
| `linux/run_collect.sh` | autolog.env を読み込んで常駐収集を起動する |
| `linux/run_import.sh` | 取り込みを実行する。`--until-now` で「いま」の2分手前まで |
| `linux/install_units.sh` | systemd のユーザーユニットを置いて有効にする |
| `linux/*.service` / `linux/*.timer` | 常駐（ログイン時）と取り込み（毎日 4:00）のユニット |

`.ps1` は UTF-8 (BOM 付き) で保存します。設定ファイルも同じです。
BOM が無いと Windows PowerShell 5.1 が Shift_JIS として読み、
日本語コメントの直後の行が黙って読み落とされます。

`.sh` は改行を LF に固定します（`.gitattributes`）。CRLF だと実行できません。
Linux の収集をサービスではなく**ユーザーのユニット**にするのは、
前面ウィンドウとセッションの状態が画面付きのログインセッションからしか
取れないためで、Windows でログオン時のタスクにしているのと同じ理由です。

ビルドはここではなく npm スクリプトが担います。

## src/tools — ビルドの小物

`package.json` の npm スクリプトから呼ばれます。

| スクリプト | 用途 |
| --- | --- |
| `build_go.mjs` | 指定したプラットフォーム向けに autolog をビルドする |
| `build_apk.mjs` | 収集アプリの APK を作る。バージョンは package.json から決まる |
| `verify_release_artifacts.mjs` | 成果物が狙ったプラットフォーム向けか、中身を見て確かめる |

### 成果物の中身を必ず確かめる

クロスコンパイルは設定を1つ間違えるだけで、中身が別プラットフォームの
バイナリのまま出来上がります。実際、Windows 上で NDK の clang を指定したときに
Android 向けのつもりが Windows のバイナリ (MZ) になったことがあります。
ファイル名では気づけないので、`verify_release_artifacts.mjs` が
先頭バイトから形式と CPU を読んで検査します。

## 実行時に作られるもの

リポジトリには入りません。

```
$AUTOLOG_HOME/                既定は %LOCALAPPDATA%\gkill_autolog（Linux は $HOME/.gkill_autolog）
├── raw.db                    生ログ（追記専用）
├── ledger.db                 書き込み済み台帳
├── url_denylist.txt          URLog にしない URL のパターン
├── notification_denylist.txt Kmemo にしない通知のパターン
├── ingest_token.txt          Chrome 拡張との共有トークン
├── collect.lock              収集の多重起動防止（残っていても無害。消さなくてよい）
├── import.lock               取り込みの多重起動防止（同上）
├── screenshots/              撮影した画像の置き場
└── logs/                     実行ログ
```

ビルドの成果物は `release/` に出ます。

```
release/
├── windows_amd64/autolog.exe
├── linux_amd64/autolog, linux_arm64/autolog, linux_arm/autolog
├── android_arm64/autolog     Termux で使う
└── android_apk/gkill_autolog.apk
```

この6つが `verify_release_artifacts.mjs` の検査対象です。

Android では共有ストレージも使います。

```
/sdcard/gkill_autolog/
├── config.env                設定（収集アプリと autolog の両方が読む）
├── crash.log                 収集アプリが落ちたときの記録。設定画面にも末尾が出る
├── audio/                    録った音声 (.m4a)。dvnf が AutoAudio へ運ぶ
├── events/                   収集アプリが置く JSONL。autolog が読んで消す
├── gpslog/                   日別の GPX。dvnf が GPSLogs へ運ぶ
└── screenshots/              撮影した画像の置き場
```

生ログと位置情報の本体は共有ストレージに置きません。
アプリ専用領域の `autolog_raw.db` と `autolog_gps.db` にあります。
共有ストレージは FUSE で、複数プロセスから SQLite を開くとロックが効かないためです。
