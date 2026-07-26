# 実装の入口

ソースの歩き方です。構成の全体像は
[folder-structure.md](../documents/reverse/folder-structure.md)、
処理の詳細は [program-spec.md](../documents/reverse/program-spec.md) を参照してください。

## どこから読むか

**まず [`autolog/internal/normalize/`](autolog/internal/normalize/) を読んでください。**

何を記録するかはすべてここで決まります。外部に依存しない純粋な処理なので、
入力と出力の対応が追いやすく、テストもここが一番厚くなっています。

次に [`autolog/cmd/autolog/cmd_import.go`](autolog/cmd/autolog/cmd_import.go) を読むと、
生ログから gkill への書き込みまでの流れが1本で追えます。

## 各ディレクトリ

| ディレクトリ | 内容 |
| --- | --- |
| [`autolog/`](autolog/) | Go の CLI。収集と取り込み。`go.mod` はここ |
| [`android/`](android/) | Android の収集アプリ。集めて共有ストレージへ書き出すだけ |
| [`chrome_ext/`](chrome_ext/) | Chrome 拡張 (Manifest V3)。閲覧と再生を送る |
| [`scripts/`](scripts/) | PowerShell スクリプト。ビルド・設定・取り込みの起動 |

## autolog のパッケージ

| パッケージ | 責務 |
| --- | --- |
| `rawlog` | 生ログ。追記専用 SQLite とイベントの型 |
| `collect` | Windows での収集 |
| `winapi` | Win32 API のラッパ |
| `shot` | スクリーンショットの撮影と WebP 変換 |
| `ingest` | Chrome 拡張からの受け口 (HTTP) |
| `inbox` | Android の収集アプリが置いた JSONL の取り込み |
| `normalize` | 生ログ → 提案。**中核** |
| `gkillclient` | gkill の HTTP API クライアント |
| `ledger` | 書き込み済み台帳 |
| `config` | 設定とディレクトリの解決 |

## ビルドとテスト

```powershell
.\scripts\build.ps1          # この機械向け
.\scripts\build_android.ps1  # Android (arm64) 向け

cd autolog
go test ./...
```

詳しくは [dev-setup.md](../documents/reverse/dev-setup.md) を参照してください。

## 書くときに気をつけること

- コメントは日本語。gkill 本体に合わせています
- **端末名・利用者名をコードに書かない。** 設定で決めます
- PowerShell スクリプトと設定ファイルは **UTF-8 (BOM 付き)** で保存します
- PowerShell は **5.1 で確認**します。7 では起きない問題があります

いずれも実際に踏んだ問題です。理由は
[design-philosophy.md](../documents/reverse/design-philosophy.md) と
[dev-setup.md](../documents/reverse/dev-setup.md) にあります。
