# テストと検証

## 単体テスト

```powershell
cd src\autolog
go test ./...
```

Android のテストも含めて回すなら、リポジトリの直下で次を実行します。

```powershell
npm test
```

| パッケージ | 何を検証しているか |
| --- | --- |
| `normalize` | **最重要。** 閾値・結合・除外の境界値と、バッチをまたぐ持ち越し・重複排除 |
| `rawlog` | 追記の冪等性、時刻の保存形式と辞書順、端末名の書式、持ち越しの往復 |
| `gkillclient` | 応答の `errors` の扱い、セッション切れの再試行、失敗の記憶 |
| `inbox` | JSONL の取り込み、書きかけの無視、壊れた行の扱い |
| `ingest` | 受け口の認証と冪等性、CORS |
| `ledger` | 書き込み済みの記録 |
| `config` | 設定の優先順位、BOM 付きファイルの読み込み、撮影間隔の丸め |
| `shot` | 撮影の区切りの計算と、ファイル名が衝突しないこと |
| `winapi` | 構造体の大きさとオフセットが Win32 の定義と合っていること（Windows のみ） |

### normalize のテストが要になる理由

何を残すかはすべてこのパッケージで決まります。
外部に依存しないので、入力を並べれば結果が決まり、境界値を正確に検証できます。

過去に見つかった不具合の例です。いずれもテストで再現できました。

- 割り込みが2回続くとウィンドウの結合に失敗する
- 動画サイトの URL が、ブラウザ経路とメディア経路の両方から記録される
- 常時つないだままの機器があると、処理カーソルが永久に止まる
- 自動再生で次へ進んだ動画が、題名が同じというだけで1本にまとまる
- 読み直した接続イベントで、終了が開始より前の区間ができる
- 結合と重複排除がバッチの切れ目で切れ、区間が割れる・同じ通知が2件になる

## 本番を汚さない検証

**本番の gkill に対して試さないでください。** 別のホームディレクトリと
別のポートで gkill を起動して検証します。

### 検証用サーバの立て方

```powershell
# 1. 検証用のバイナリをビルドする（本番のバイナリを上書きしないよう別名で）
cd <gkill のリポジトリ>\src\server
go build -o <作業用ディレクトリ>\gkill_server_verify.exe .\gkill\main\gkill_server

# 2. 別のホームと別のポートで起動する
<作業用ディレクトリ>\gkill_server_verify.exe `
    --gkill_home_dir <作業用ディレクトリ>\gkill_verify `
    --address 127.0.0.1:19998 --disable_tls
```

`--address` は設定ファイルの値を書き換えない実行時の指定なので、本番に影響しません。

### 初回のアカウント設定

管理者アカウントは初回起動で作られますが、**既定のリポジトリは作られません。**

```powershell
# reset_token を取り出す
curl -i http://127.0.0.1:19998/
#   → Location: /regist_first_account?reset_token=<トークン>

# パスワードを設定する
curl -X POST http://127.0.0.1:19998/api/set_new_password `
     -H 'Content-Type: application/json' `
     -d '{"user_id":"admin","reset_token":"<トークン>","new_password_sha256":"<64桁>","locale_name":"ja"}'
```

端末別ユーザーは `/api/add_user` に `do_initialize: true` で作ります。
このとき既定のリポジトリも作られます。

### 取り込みの検証

```powershell
# 生ログを検証用にコピーしてから試す
$env:AUTOLOG_HOME = '<作業用ディレクトリ>\autolog_verify'
$env:GKILL_BASE_URL = 'http://127.0.0.1:19998'

.\autolog.exe import --dry-run --until-now   # 発行予定を見る
.\autolog.exe import --until-now             # 実際に書く
.\autolog.exe import --until-now             # 2回目は0件になるはず
```

確認すること。

1. 件数と時刻が生ログと一致するか
2. `create_app` が `gkill_autolog`、`create_device` が端末名になっているか
3. 端末ごとに正しいユーザーへ書き分けられているか
4. 2回流しても増えないか（台帳が効いているか）
5. URLog を書いたとき、gkill から外向きの通信が1件につき1回だけか

書き込まれた内容は SQLite を直接見るのが確実です。

```powershell
sqlite3 <gkill_verify>\datas\<ユーザー>\TimeIs.db `
    "select CREATE_APP, CREATE_DEVICE, TITLE, START_TIME from TIMEIS;"
```

### 後片付け

検証用サーバは必ず止めてください。本番のポートが空いていることも確かめます。

```powershell
Get-Process gkill_server_verify -ErrorAction SilentlyContinue | Stop-Process -Force
Get-NetTCPConnection -State Listen -LocalPort 9999 | Select-Object OwningProcess
```

## PowerShell スクリプトの検証

**Windows PowerShell 5.1 で確認してください。** 7 では起きない問題があります。

```powershell
powershell.exe -NoProfile -File .\src\scripts\check_connection.ps1
```

`-File` で起動することにも意味があります。5.1 ではこの起動方法のときだけ
param ブロックの `$PSScriptRoot` が空になります。タスクスケジューラも `-File` を使います。

詳しくは [dev-setup.md](dev-setup.md) を参照してください。

## 実機での確認

### 収集が動いているか

```powershell
.\autolog.exe status                    # 種別ごとの件数と処理カーソル
.\autolog.exe status --since 1h --dump  # 直近1時間のイベントの中身
```

Chrome 拡張が届いているかは `browser_view` と `media_play` の件数で分かります。

### Android

```sh
# 生ログが渡っているか
ls /sdcard/gkill_autolog/events/

# 取り込まれているか
ls -la ~/gkill/datas/<接頭辞><端末名>/

# 運ばれているか
ls "$(gkill_server dvnf get)" | grep Auto
```

書き出したファイルは取り込み後に消えます。
`events/` が空なら、渡すものが無いか、まだ書き出していないかのどちらかです。

撮影や取り込みが動かないときは、切り分け用の情報をまとめて集められます。

```sh
sh src/scripts/collect_android_diag.sh   # /sdcard/gkill_autolog_diag.txt に出る
```

**中身を見てから渡してください。** アプリ名や通知の断片がログに含まれることがあります。

## やってはいけないこと

- **本番の gkill に対して取り込みを試す。** 消すのが面倒です
- **`update_user_reps` を手で叩く。** 全件置換なので既存のリポジトリ設定が飛びます
- **確認スクリプトを繰り返す。** ログインは IP ごとに 15 分で 10 回までです
