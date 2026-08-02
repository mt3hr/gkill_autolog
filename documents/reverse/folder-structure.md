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
│   ├── collect/          Windows での収集（ウィンドウ・セッション・Wi-Fi 等）
│   ├── winapi/           Win32 API のラッパ
│   ├── shot/             スクリーンショットの撮影と WebP 変換
│   ├── ingest/           Chrome 拡張からの受け口 (HTTP)
│   ├── inbox/            Android の収集アプリが置いた JSONL の取り込み
│   ├── normalize/        生ログ → 提案。ルール処理の中核
│   ├── gkillclient/      gkill の HTTP API クライアント。書き込みの制御もここ
│   ├── ledger/           書き込み済み台帳
│   ├── proclock/         多重起動を防ぐ OS ファイルロック
│   └── config/           設定とディレクトリの解決
└── schema/
    └── event.schema.json 生ログのスキーマ
```

### プラットフォーム別のファイル

Windows でしか動かない収集は `_windows.go` で分けています。
他のプラットフォームでは `run_other.go` が「収集しない」実装を提供するので、
Android 向けにビルドしても壊れません。

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
├── AutologService.kt      常駐して各収集を回す
├── MainActivity.kt        設定と権限付与の画面
├── BootReceiver.kt        再起動後の再開
├── Config.kt              設定。端末名は config.env を優先する
├── SharedStorage.kt       /sdcard/gkill_autolog の場所
├── collect/               各収集（アプリ利用・通知・メディア・システム・撮影・位置情報）
├── export/                JSONL と GPX の書き出し
├── model/Event.kt         生ログの1件
└── store/                 書き出すまでの一時保管（生ログ・位置情報）
```

## src/chrome_ext — Chrome 拡張

Manifest V3。閲覧したページと、動画・音楽の実再生時間を送ります。

```
chrome_ext/
├── manifest.json    権限と読み込むファイル
├── background.js    Service Worker。閲覧区間の管理と送信
├── content_media.js 各ページで再生を数える
├── options.html     送信先と共有トークンの設定画面
└── options.js
```

Service Worker は随時停止するので、イベントはいったん `chrome.storage` へ積み、
`chrome.alarms` でまとめて送ります。送れた分だけ消します。
積める上限は5000件で、あふれたら古いものから捨てます。

## src/scripts — PowerShell スクリプト

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

スクリプトは UTF-8 (BOM 付き) で保存します。設定ファイルも同じです。
BOM が無いと Windows PowerShell 5.1 が Shift_JIS として読み、
日本語コメントの直後の行が黙って読み落とされます。

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
$AUTOLOG_HOME/                既定は %LOCALAPPDATA%\gkill_autolog
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
├── events/                   収集アプリが置く JSONL。autolog が読んで消す
├── gpslog/                   日別の GPX。dvnf が GPSLogs へ運ぶ
└── screenshots/              撮影した画像の置き場
```

生ログと位置情報の本体は共有ストレージに置きません。
アプリ専用領域の `autolog_raw.db` と `autolog_gps.db` にあります。
共有ストレージは FUSE で、複数プロセスから SQLite を開くとロックが効かないためです。
