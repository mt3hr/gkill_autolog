# 導入と運用

各端末が自分で集め、自分の gkill へ取り込みます。端末をまたぐ転送はしません。

このページでは端末名を `Laptop`（PC）と `Phone`（Android）、
gkill の利用者名を `myuser` として書きます。実際の値に読み替えてください。

## 共通の準備

### 1. gkill に端末別ユーザーを作る

gkill の追加 API は書き込み先リポジトリを指定できないため、
端末ごとに書き分けるにはユーザーを分けます。

**gkill は端末ごとに別インスタンスです。** その端末の gkill に作ってください。
他の端末の gkill に作っても、その端末からは使えません。

ユーザー名は `<接頭辞><端末名>` です。端末名は
`gkill_server dvnf get` が返すディレクトリ名と揃えます。

```powershell
.\src\scripts\setup_auto_users.ps1 -UserPrefix myuser_auto_ -Devices Laptop
```

管理者アカウントが要ります。`do_initialize` で既定のリポジトリも作られるので、
置き場所は既定のままで構いません。

### 2. 閲覧用の設定を追加する

普段使っている gkill のユーザーから見えるように、リポジトリを追加します。
**読み取り専用**（`use_to_write` はオフ）にしてください。
オンにすると同じ種別の書き込み先が二重になり、gkill が起動時にエラーになります。

| 種別 | ファイル |
| --- | --- |
| timeis | `$HOME/Kyou/AutoTimeIs_*.db` |
| urlog | `$HOME/Kyou/AutoURLog_*.db` |
| kmemo | `$HOME/Kyou/AutoKmemo_*.db` |
| tag | `$HOME/Kyou/AutoTag_*.db` |
| directory | `$HOME/Kyou/AutoScreenshot_*` |

## PC (Windows) の導入

### 設定ファイル

`src/scripts/autolog.env.example` を `autolog.env` としてコピーし、値を埋めます。
**UTF-8 (BOM 付き)** で保存してください。

```
AUTOLOG_DEVICE=Laptop
AUTOLOG_USAGE_TITLE=Windows利用
GKILL_BASE_URL=https://127.0.0.1:9999
GKILL_INSECURE=true
GKILL_AUTO_USER_PREFIX=myuser_auto_
GKILL_AUTO_PASSWORD_SHA256=
```

スクリーンショットの撮影間隔を変えたいときは次の1行を足します。
省略すると1時間（毎時00分）です。`1h` `30m` `15m` のように書き、
単位を書かなければ秒として読みます。10秒〜24時間の範囲へ丸められます。

```
AUTOLOG_SCREENSHOT_INTERVAL=15m
```

一度だけ試すなら `autolog collect --screenshot-interval 15m` でも指定できます。
フラグのほうが設定ファイルより優先されます。撮影そのものを止めるのは
`--no-screenshot` です。

パスワードは対話で設定します。平文は書きません。

```powershell
.\src\scripts\set_auto_password.ps1
```

全ユーザーにログインできることを確かめてから保存します。
間違ったまま保存して、取り込みが静かに失敗し続けるのを防ぐためです。

### ビルドと確認

```powershell
npm run build
.\src\scripts\check_connection.ps1        # 書き込まない
.\src\scripts\run_import.ps1 -DryRun -UntilNow
```

### 常駐と定期実行

```powershell
.\src\scripts\register_tasks.ps1 -WhatIfOnly   # 内容の確認
.\src\scripts\register_tasks.ps1
```

収集はサービスではなくログオン時のタスクにします。
前面ウィンドウとセッションの状態は、対話セッションに属するプロセスからしか取れません。

同期スクリプトから取り込みを呼んでいるなら、定期実行のタスクは不要です。

### 同期スクリプトへの組み込み

gkill の同期スクリプトに、取り込みと運搬を足します。

```powershell
# 自動操作ログを取り込む。失敗しても以降の同期は続ける。
& "$HOME/Git/gkill_autolog/src/scripts/run_import.ps1" -UntilNow
if ($LASTEXITCODE -ne 0) { echo "取り込みに失敗しました。次回やり直されます。" }

# 端末別ユーザーの rep を運ぶ。端末名はアカウント名から取り出す。
Get-ChildItem (Join-Path $HOME "gkill/datas") -Directory -Filter "myuser_auto_*" | ForEach-Object {
    $auto_device = $_.Name -replace '^myuser_auto_', ''
    foreach ($auto_db in 'TimeIs', 'URLog', 'Kmemo', 'Tag', 'Text') {
        $auto_src = Join-Path $_.FullName "$auto_db.db"
        if (Test-Path $auto_src) {
            gkill_server dvnf copy -f --device $auto_device $auto_src "Auto$auto_db.db"
        }
    }
}

# スクリーンショット
gkill_server dvnf move '$LOCALAPPDATA/gkill_autolog/screenshots/*.webp' AutoScreenshot
gkill_server idf (gkill_server dvnf get AutoScreenshot)
```

## Chrome 拡張

`chrome://extensions` を開き、デベロッパーモードで `src/chrome_ext` を読み込みます。

オプション画面で受け口の URL と共有トークンを設定します。
トークンは `autolog collect` の初回起動時に作られ、
`$AUTOLOG_HOME/ingest_token.txt` に保存されています。

## Android の導入

gkill を Termux で動かしている前提です。

```
収集アプリ ──→ /sdcard/gkill_autolog/events/*.jsonl ──→ autolog import ──→ その端末の gkill
           ├─→ /sdcard/gkill_autolog/screenshots/*.webp ──→ dvnf ──→ AutoScreenshot_<端末>_<日付>
           └─→ /sdcard/gkill_autolog/gpslog/YYYYMMDD.gpx ──→ dvnf ──→ GPSLogs_<端末>_<日付>
```

### 1. 設定ファイルを置く

`/sdcard/gkill_autolog/config.env` を作ります。収集アプリと `autolog` の両方が読みます。

```
GKILL_BASE_URL=https://127.0.0.1:9999
GKILL_INSECURE=true
GKILL_AUTO_USER_PREFIX=myuser_auto_
GKILL_AUTO_PASSWORD_SHA256=<64桁の16進>
AUTOLOG_DEVICE=Phone
```

端末ごとに変えるのは `AUTOLOG_DEVICE` の1行だけです。

```sh
# ハッシュの作り方
printf '%s' 'パスワード' | sha256sum | cut -d' ' -f1
```

### 2. autolog を入れる

PC でビルドして配り、端末で取り込みます。

```powershell
npm run deploy_android
```

```sh
~/.termux/tasker/update_autolog.sh
```

### 3. アプリを入れて権限を与える

`src/android/app/build/outputs/apk/debug/app-debug.apk` を入れ、画面から順に許可します。

| 権限 | 何に要るか | 無いとどうなるか |
| --- | --- | --- |
| **全ファイルアクセス** | 生ログの書き出し | 端末に溜まったまま渡らない |
| 使用状況へのアクセス | アプリ利用の記録 | アプリ利用が記録されない |
| 通知へのアクセス | 通知・再生情報 | 通知と再生が取れない |
| 位置情報 | Wi-Fi の SSID、GPX | SSID が空になり、位置情報も取れない |
| 位置情報を「常に許可」 | 画面が消えている間の GPX | 画面を消すと位置情報が途切れる |
| バッテリー最適化の対象外 | 常駐 | 収集が止まる |

全ファイルアクセスだけは他に手段がありません。
生ログの渡し先が Termux の `autolog` と共有する場所で、アプリ専用領域では渡せないためです。

「収集を開始」で常駐が始まります。書き出しは1時間おきで、
すぐ渡したいときは「今すぐ書き出し」を押します。

### スクリーンショット (Android)

「スクリーンショットを定期的に撮る」をオンにすると、
`/sdcard/gkill_autolog/screenshots/` へ置きます。**root が要ります**
（`su -c screencap` を使う。root が無い端末では何も起きません）。

撮影間隔は既定60分（毎時00分）で、1〜1440分の範囲で変えられます。
撮影時刻は間隔で丸めるので、15分にすると毎時00分・15分・30分・45分になります。
画面が消えている間とロック中は撮りません。

**「撮り逃したら次に画面を点けたときに撮る」は入れたままにしてください**（既定は有効）。
スマホは大半の時間で画面が消えているので、これを外すと
「区切りの時刻の5秒以内に画面が点いていた」ときしか撮れず、ほとんど残りません。
実際、これが無かったころは1日に数枚しか撮れていませんでした。

撮り直したぶんの記録時刻は、区切りの時刻ではなく**実際に撮れた時刻**です。
ファイル名も `Phone_2026-07-29_20-37-12.webp` のように半端な時刻になります。
撮れていない時間の画像をでっち上げないためで、
「撮り逃した時間の画像を後から補完しない」という取り決めは守られています。

借りとして持つのは最後の1区切りだけです。何時間も画面を消していたあとに
点けても、まとめて何枚も撮ることはありません。

PC 側と違い `config.env` は見ません。撮るのは収集アプリで、
Termux の `autolog` は撮らないためです。

### 位置情報 (GPX)

「位置情報を記録する」をオンにすると、日別の `YYYYMMDD.gpx` を
`/sdcard/gkill_autolog/gpslog/` に書きます。記録間隔は既定60秒で、
10〜3600秒の範囲で変えられます。短くするほど経路は細かくなりますが電池を使います。

記録間隔ごとに、その間隔の中で**いちばん精度の良い測位だけ**を残します。
屋内では GPS が入らずセル測位しか取れないことがあり、そのまま記録すると
経路が数百m〜km 単位で飛びます。精度に関わる設定が2つあります。

| 設定 | 既定 | 何が変わるか |
| --- | --- | --- |
| 許容誤差 | 100m | これより粗い測位は記録しない。取れなかった時間は点が空くだけで、埋めない |
| 高精度モード | オン | 記録間隔より短い周期で測位して候補を増やす。精度は上がるが電池を使う |

高精度モードをオフにしても「いちばん精度の良い点を残す」動きは残ります。
電池が気になるときはオフにしてください。

経路が飛ぶときは許容誤差を小さく（例: 50m）、
点が空きすぎるときは大きく（例: 200m）します。
`adb logcat -s AutologLocation` に、捨てた測位とその誤差が出ます。

**「常に許可」が要ります。** 通常の許可ダイアログでは選べないので、
画面の「位置情報を「常に許可」にする」からアプリの設定を開いて選んでください。
これが無いと、画面が消えている間の位置情報が取れません。

GPX は書くたびにその日の全点から作り直し、いったん `.tmp` へ書いてから
名前を変えます。読む側からは常に出来上がったファイルしか見えないので、
書きかけを dvnf がコピーする心配はありません。

**ファイル名は日付だけなので、他の記録アプリと併用すると衝突します。**
実際、他の記録アプリと併用していた日に、後から運ばれた autolog の
ファイルがその1日分を上書きしました。どちらか一方に寄せてください。

以前の記録を引き継ぎたい場合は、点を autolog のデータベース
（アプリ専用領域の `autolog_gps.db`）へ入れると、autolog 自身が
まとめて1つの GPX を出すようになります。運搬先へ直接置いても、
autolog が次に書き出したときに上書きされます。

### 4. 取り込む

```sh
autolog import --dry-run --until-now   # 確認
~/.termux/tasker/autolog.sh            # 実行
```

その端末の `gkill_server` が動いている必要があります。

### 5. 同期スクリプトへの組み込み

端末名を決め打ちしないので、どの端末でも同じ内容で動きます。

```sh
# 端末別ユーザーの rep を運ぶ
auto_device=$(basename "$(gkill_server dvnf get)")
auto_user="myuser_auto_$auto_device"
for auto_db in TimeIs URLog Kmemo Tag Text; do
  auto_src="$HOME/gkill/datas/$auto_user/$auto_db.db"
  if [ -f "$auto_src" ]; then
    gkill_server dvnf copy -f "$auto_src" "Auto$auto_db.db"
  fi
done

# スクリーンショット
gkill_server dvnf move "$HOME/storage/shared/gkill_autolog/screenshots/*" AutoScreenshot
gkill_server idf $(gkill_server dvnf get AutoScreenshot)

# 位置情報。gkill 側の rep 登録は既存の $HOME/Kyou/GPSLogs_* のままでよい。
gkill_server dvnf copy "$HOME/storage/shared/gkill_autolog/gpslog/*" GPSLogs
```

### root が無い端末

スクリーンショットと Chrome 履歴は取れません。
アプリ利用・通知・Wi-Fi・充電・端末利用・位置情報は取れます。

## 端末を増やすとき

1. その端末の gkill に `<接頭辞><端末名>` のユーザーを作る
2. その端末の `config.env` に `AUTOLOG_DEVICE=<端末名>` を書く
3. `autolog` と（Android なら）アプリを入れる

同期スクリプトは変更しなくて構いません。端末名は `dvnf get` から取ります。

## 何を記録するか変えたいとき

### URL を除外する

`$AUTOLOG_HOME/url_denylist.txt` に書きます。1行1パターンです。

- そのまま書くと部分一致（大文字小文字は区別しない）
- `re:` で始めると正規表現

初回の実行時に既定の内容で作られます。

### 端末利用のタイトル

`AUTOLOG_USAGE_TITLE` で決めます。省略すると「端末利用」です。

## うまくいかないとき

### 取り込みが0件

上限時刻は既定で「直近の午前4時」です。素の `autolog import` を手で実行すると、
その日に集めた分がまるごと対象外になります。

同期スクリプトから呼ばれる `run_import.ps1 -UntilNow` と `autolog.sh` は、
どちらも上限を**2分前**にしているのでこの問題は起きません。
手で確かめるときは `autolog import --until-now` を使ってください。

上限を「いま」ちょうどにせず2分手前にしているのは、接続系の区間が割れるのを防ぐためです。
Wi-Fi・Bluetooth・充電は短い切断を結合しますが、結合は次の接続を見て初めて判定されます。
切断直後に処理すると本来つながる区間が2つになり、書き込むと台帳に載るので後から直りません。
2分あれば最長のマージ窓（1分）を確実に超えます。

### 「台帳で除外」ばかりで書き込みが0件

異常ではありません。台帳へは**書き込みに成功した分だけ**記録するので、
除外されたということは既に gkill へ入っています。

端末で取り込んだデータは、その端末の gkill の `myuser_auto_<端末名>` ユーザー配下にあります。
`myuser` でログインしていると見えません。PCへ出てくるのは `dvnf.sh` を回した後です。

### 書き出しを要求できない

`autolog.sh` は取り込みの前に収集アプリへ書き出しをさせます。
記録はアプリ内のDBに溜まっていて、書き出すまで共有ディレクトリには出てこないためです。

```
Broadcast completed: result=12, data="12 件を書き出した"
```

`data` が出ていれば書き出しの完了まで待てています。出ない場合は次を確認してください。

| 症状 | 原因 |
|---|---|
| `Could not connect to socket` | `termux-am` のソケットが無い。`/system/bin/am` へ落とす |
| `Broadcast sent without waiting for result` | 結果を待たずに戻っている。送信自体は成立している |
| `SecurityException: ... user -2 ... INTERACT_ACROSS_USERS` | `--user 0` の指定漏れ |
| `result=-1` | 全ファイルアクセスの許可が外れている |
| `result=0` | 溜まっている記録が無い。収集サービスが止まっていないか確認する |

`termux-am` のソケットは `termux-am-socket` パッケージではなく **Termux アプリ本体**が作ります。
パスは `/data/data/com.termux/files/apps/com.termux/termux-am/am.sock` で、`$PREFIX/var/run/` ではありません。

**F-Droid の Termux 0.118.3 はこのサーバを持ちません。** ディレクトリごと存在せず、
`TERMUX_APP__AM_SOCKET_SERVER_ENABLED` も export されないので、`termux.properties` で有効にすることもできません。
Android 17 の端末で確認しています。この場合は `/system/bin/am` を使ってください。
`--user 0` さえ付ければ順序付きブロードキャストも結果待ちも問題なく動きます。

### `Client sent an HTTP request to an HTTPS server.`

`GKILL_BASE_URL` が `http://` になっています。`https://` にしてください。
自己署名証明書なら `GKILL_INSECURE=true` も要ります。

### `ERR000374 ログイン試行回数の上限`

gkill のログインは **IP ごとに 15 分で 10 回**までです。ユーザー単位ではありません。
確認スクリプトを繰り返すとすぐ上限に達します。15 分待ってください。

書けなかった分は台帳に載らないので、次の実行でやり直されます。

### 設定が読まれていない

設定ファイルが **UTF-8 (BOM 付き)** か確認してください。
BOM が無いと PowerShell 5.1 が Shift_JIS として読み、
日本語コメントの直後の行が黙って消えます。

### 時刻が UTC になる (Android)

`autolog` が古い可能性があります。Go は Android で `time.Local` を UTC に固定するため、
自分でタイムゾーンを設定する必要があります。最新のバイナリを入れてください。
