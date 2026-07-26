# フォルダ構成

```
gkill_autolog/
├── README.md                概要と最短の導入手順
├── LICENSE                  MIT
├── CLAUDE.md                Claude Code 向けの案内
├── documents/
│   └── reverse/             設計資料（このディレクトリ）
└── src/
    ├── README.md            実装の入口
    ├── autolog/             Go の CLI（収集・取り込み）
    ├── android/             Android 収集アプリ
    ├── chrome_ext/          Chrome 拡張
    └── scripts/             PowerShell スクリプト
```

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
│   ├── gkillclient/      gkill の HTTP API クライアント
│   ├── ledger/           書き込み済み台帳
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
├── collect/               各収集（アプリ利用・通知・メディア・システム・撮影）
├── export/                JSONL の書き出し
├── model/Event.kt         生ログの1件
└── store/EventStore.kt    書き出すまでの一時保管
```

## src/chrome_ext — Chrome 拡張

Manifest V3。閲覧したページと、動画・音楽の実再生時間を送ります。

Service Worker は随時停止するので、イベントはいったん `chrome.storage` へ積み、
`chrome.alarms` でまとめて送ります。送れた分だけ消します。

## src/scripts — PowerShell スクリプト

| スクリプト | 用途 |
| --- | --- |
| `_gkill_api.ps1` | 共通処理。gkill の API 呼び出し、設定ファイルの読み書き、パスの解決 |
| `build.ps1` | この機械向けにビルドする |
| `build_android.ps1` | Android (arm64) 向けにビルドして配布する |
| `setup_auto_users.ps1` | 端末別ユーザーを作る |
| `set_auto_password.ps1` | 端末別ユーザー共通のパスワードを設定する |
| `set_password.ps1` | 単一ユーザー構成のパスワードを設定する |
| `check_connection.ps1` | gkill へ繋がるか確かめる（書き込まない） |
| `run_import.ps1` | 取り込みを実行する。同期スクリプトから呼ぶ |
| `register_tasks.ps1` | タスクスケジューラへ登録する |
| `autolog.env.example` | 設定ファイルの雛形 |

スクリプトは UTF-8 (BOM 付き) で保存します。設定ファイルも同じです。
BOM が無いと Windows PowerShell 5.1 が Shift_JIS として読み、
日本語コメントの直後の行が黙って読み落とされます。

## 実行時に作られるもの

リポジトリには入りません。

```
$AUTOLOG_HOME/                既定は %LOCALAPPDATA%\gkill_autolog
├── raw.db                    生ログ（追記専用）
├── ledger.db                 書き込み済み台帳
├── url_denylist.txt          URLog にしない URL のパターン
├── ingest_token.txt          Chrome 拡張との共有トークン
├── screenshots/              撮影した画像の置き場
└── logs/                     実行ログ
```

Android では共有ストレージも使います。

```
/sdcard/gkill_autolog/
├── config.env                設定（収集アプリと autolog の両方が読む）
├── events/                   収集アプリが置く JSONL。autolog が読んで消す
└── screenshots/              撮影した画像の置き場
```
