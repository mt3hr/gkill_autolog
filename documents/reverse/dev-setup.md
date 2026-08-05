# 開発環境の構築

## 必要なもの

| 対象 | 必要なもの |
| --- | --- |
| ビルドとテストの実行 | Node.js 20 以上 (npm)。ビルドは npm スクリプト経由で行う |
| Go の CLI | Go 1.26 以上 |
| Android アプリ | JDK 17、Android SDK (compileSdk 37。minSdk 26 / targetSdk 36) |
| Chrome 拡張 | なし（そのまま読み込める。テストは Node の標準ランナー） |
| スクリプト | Windows PowerShell 5.1 または PowerShell 7 |
| 端末別ユーザーの作成 | PATH に通った `sqlite3` |

**最初に一度 `npm install` を実行してください。** ビルドの npm スクリプトは
`cross-env` などの devDependencies を使うため、これが無いと最初の
`npm run build` が失敗します。

CGO は使いません。SQLite は純 Go の実装なので、C コンパイラは要りません。

## ビルド

### この機械向け

```powershell
npm run build
```

`release/windows_amd64/autolog.exe` ができます。取り込みスクリプトはこの場所を見ます。

直接叩く場合は次のとおりです。

```powershell
cd src\autolog
go build -o ..\..\release\windows_amd64\autolog.exe .\cmd\autolog
```

### Android (arm64) 向け

```powershell
npm run build_android_arm64
```

`release/android_arm64/autolog` ができます。
端末への入れ方は [operations-guide.md](operations-guide.md) を参照してください。

**`CGO_ENABLED=0` が必要です。** Windows 上で NDK の clang を指定すると、
Go がネイティブのコンパイラへ切り替わり、中身が Windows のバイナリ (MZ) のまま
出来上がることがあります。

出力が本当に ARM64 の ELF かは `verify_release_artifacts.mjs` が確認しますが、
**単発のビルドでは走りません。** 端末へ配る前に確認してください。

```powershell
npm run verify_release_artifacts   # npm run release なら最後に自動で走る
```

### Android アプリ

```powershell
cd src\android
.\gradlew.bat --% assembleDebug -PversionName=1.0.0 -PversionCode=10000
```

PowerShell からハイフンを含む値を渡すときは `--%` が要ります。
付けないと PowerShell が引数として解釈します。

## テスト

```powershell
cd src\autolog
go test ./...
```

`internal/normalize` のテストが最も重要です。閾値・結合・持ち越しの境界値を検証しています。

Chrome 拡張のテストは Node の標準ランナーで回します。
`npm test` は Go と Chrome 拡張の両方を回します。

```powershell
npm run test_chrome_ext
```

Android 向けにビルドが通ることも確認してください。

```powershell
npm run vet_android
```

**環境変数を手で設定して `go vet` を叩かないでください。**
`$env:GOOS='android'` はそのセッションに残るので、以後の `go test` や
`go build` が Android 向けになって通らなくなります。上の npm スクリプトは
`cross-env` で子プロセスにだけ渡すため、この事故が起きません。

## コードの書き方

gkill 本体に合わせています。

- コメントは日本語
- `slices.SortFunc`（`sort.Slice` は使わない）
- `for range n`（`for i := 0; i < n; i++` は使わない）
- `any`（`interface{}` は使わない）
- 複数のエラーをまとめるときは `errors.Join`

### 端末名や利用者名をコードに書かない

設定で決めます。特定の環境の名前は、既定値としても置きません
（ホスト名・機種名から導くような環境非依存の既定は構いません）。
コードに書くと、他の人がそのまま使えなくなります。

### エラーメッセージの言語

利用者が直接目にする経路（`config`・`cmd`・`gkillclient`・`inbox`）は日本語です。
内部診断だけの低層（`winapi` や `rawlog` の SQL 周辺など）は英語のままにしています。

### PowerShell スクリプト

**UTF-8 (BOM 付き) で保存してください。** 設定ファイルも同じです。

BOM が無いと Windows PowerShell 5.1 が Shift_JIS として読みます。
日本語コメントのバイト列が2バイト文字として解釈されると、
その2バイト目が改行を飲み込み、**次の行がコメント行に連結されて消えます。**

5.1 と 7 の差で踏みやすい点が他にもあります。

| 事象 | 対処 |
| --- | --- |
| `-SkipCertificateCheck` が無い (5.1) | `_gkill_api.ps1` の `Invoke-GkillApi` を使う |
| `-File` 起動で param ブロックの `$PSScriptRoot` が空 (5.1) | 既定値は param に書かず本体で解決する |
| `2>&1` した native コマンドの stderr が終了エラーになる (5.1) | 呼び出しの間だけ `$ErrorActionPreference='Continue'` にし、終了コードで判定する |
| 子プロセスの UTF-8 出力が化ける (5.1) | 呼び出しの間だけ `[Console]::OutputEncoding` を UTF-8 にする |

いずれも PowerShell 7 では起きないので、**5.1 で確認してください。**

```powershell
powershell.exe -NoProfile -File .\src\scripts\check_connection.ps1
```

## 本番を壊さないための注意

- gkill の設定を書き換える API (`update_user_reps` など) は全件置換です。手で使わないでください
- 検証は別のホームディレクトリと別のポートで起動した gkill に対して行います
  （[testing-guide.md](testing-guide.md) 参照）
- gkill のログインは IP ごとに 15 分で 10 回までです。確認スクリプトの連打に注意してください
