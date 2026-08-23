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

**PC (Windows) の gkill には、付属のスクリプトを使います。**

```powershell
.\src\scripts\setup_auto_users.ps1 -AdminUser admin -UserPrefix myuser_auto_ -Devices Laptop
```

`-AdminUser` には gkill の管理者アカウント名を渡します（必須。省略すると
文脈の無いプロンプトが出ます）。パスワードは実行中に聞かれ、画面には出ません。
ほかに PATH に通った `sqlite3.exe` が要ります
（既存のアカウントと発行済みのリセットトークンを `account.db` から直接読むため）。
`do_initialize` で既定のリポジトリも作られるので、置き場所は既定のままで構いません。

**Android (Termux の gkill) では、このスクリプトは使えません。**
PowerShell と PC 側の `account.db` を前提にしているためです。`-BaseUrl` で
Termux の gkill を指しても、リセットトークンは PC 側の DB から読むので成立しません。
Termux 側では次のように API を直接叩きます（`curl` は本物なのでそのまま使えます）。

```sh
# 1. 管理者でログインして session_id を得る
curl -sk -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d '{"user_id":"admin","password_sha256":"<64桁>","locale_name":"ja"}'

# 2. 端末別ユーザーを作る（既定のリポジトリも一緒に作られる）
curl -sk -X POST "$BASE/api/add_user" -H 'Content-Type: application/json' \
  -d '{"session_id":"<session_id>","do_initialize":true,"locale_name":"ja",
       "account_info":{"user_id":"myuser_auto_Phone","is_admin":false,"is_enable":true}}'

# 3. 発行されたリセットトークンでパスワードを設定する
sqlite3 ~/gkill/configs/account.db \
  "select USER_ID, PASSWORD_RESET_TOKEN from ACCOUNT;"
curl -sk -X POST "$BASE/api/set_new_password" -H 'Content-Type: application/json' \
  -d '{"user_id":"myuser_auto_Phone","reset_token":"<トークン>",
       "new_password_sha256":"<64桁>","locale_name":"ja"}'
```

`$BASE` はその端末の gkill（例 `https://127.0.0.1:9999`）です。
**応答が HTTP 200 でも `errors` が入っていることがあります。** 必ず中身を見てください。
逆に 4xx/5xx でも、理由（`error_code`）は本文の `errors` にしか入っていません。
`curl` で確かめるときは `-i` を付けてステータスと本文の両方を見てください。
また **ログインは IP ごとに 15 分で 10 回まで**（成功も数えられます）なので、
失敗しても続けて叩き直さないでください。

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
| directory | `$HOME/Kyou/AutoAudio_*`（Android の録音を使う場合） |

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

autolog.env は `run_collect.ps1` / `run_import.ps1` が読み込みます。
古い登録（autolog.exe を直接起動するタスク）のままだと常駐収集には効かないので、
変えても反映されない場合は `register_tasks.ps1` を実行し直してください。

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
.\src\scripts\register_tasks.ps1 -Unregister   # 消すとき
```

`gkill_autolog_collect`（ログオン時に常駐）と
`gkill_autolog_import`（毎日4:00）の2つが作られます。管理者権限は要りません。

収集はサービスではなくログオン時のタスクにします。
前面ウィンドウとセッションの状態は、対話セッションに属するプロセスからしか取れません。

同期スクリプトから取り込みを呼んでいるなら、`gkill_autolog_import` は不要です。

### 同期スクリプトへの組み込み

gkill の同期スクリプトに、取り込みと運搬を足します。

```powershell
# 自動操作ログを取り込む。失敗しても以降の同期は続ける。
& "$HOME/Git/gkill_autolog/src/scripts/run_import.ps1" -UntilNow
if ($LASTEXITCODE -ne 0) { echo "取り込みに失敗しました。次回やり直されます。" }

# 端末別ユーザーの rep を運ぶ。端末名はアカウント名から取り出す。
Get-ChildItem (Join-Path $HOME "gkill/datas") -Directory -Filter "myuser_auto_*" | ForEach-Object {
    $auto_device = $_.Name -replace '^myuser_auto_', ''
    foreach ($auto_db in 'TimeIs', 'URLog', 'Kmemo', 'Tag') {
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

オプション画面で Chrome の受け口（`autolog collect` が開く HTTP サーバ）の URL と
共有トークンを設定します。トークンは `autolog collect` の初回起動時に作られ、
`$AUTOLOG_HOME/ingest_token.txt` に保存されています。

## Android の導入

gkill を Termux で動かしている前提です。

```
収集アプリ ──→ /sdcard/gkill_autolog/events/*.jsonl ──→ autolog import ──→ その端末の gkill
           ├─→ /sdcard/gkill_autolog/screenshots/*.webp ──→ dvnf ──→ AutoScreenshot_<端末>_<日付>
           ├─→ /sdcard/gkill_autolog/audio/*.m4a ─────────→ dvnf ──→ AutoAudio_<端末>_<日付>
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

PC でビルドして端末へ送り、Termux の PATH に置きます。

```powershell
npm run build_android_arm64
adb push release/android_arm64/autolog /sdcard/autolog
```

```sh
# Termux 側
cp /sdcard/autolog "$PREFIX/bin/autolog" && chmod +x "$PREFIX/bin/autolog" && rm /sdcard/autolog
autolog status   # 動くことの確認
```

配り方は問いません。端末に arm64 の ELF が届いて実行できればそれで足ります。

### 3. アプリを入れて権限を与える

```powershell
npm run build_android_apk
npm run install_apk        # adb install。手で入れるなら release/android_apk/gkill_autolog.apk
```

APK は release ビルドで、署名鍵の用意が要ります
（[dev-setup.md](dev-setup.md) 参照）。

**debug ビルドの APK が既に入っている端末では、署名が違うため上書きできません。**
一度アンインストールする必要があり、アプリ内に溜まったままの記録は消えます。
入れ替える前に書き出しと取り込みを済ませてください。

```sh
# 1. 端末で「今すぐ書き出し」を押す（またはブロードキャストで書き出させる）
am broadcast --user 0 -f 0x20 -n com.mt3hr.gkill_autolog/.ExportReceiver

# 2. 取り込む（events/ が空になることを確かめる）
autolog import --until-now
ls /sdcard/gkill_autolog/events/

# 3. 消して入れ直す。設定 (config.env) と共有ストレージの中身は残る
```

`/sdcard/gkill_autolog/` はアプリ専用領域ではないので、
アンインストールしても設定・スクリーンショット・GPX は消えません。

画面から順に許可します。

| 権限 | 何に要るか | 無いとどうなるか |
| --- | --- | --- |
| **全ファイルアクセス** | 生ログの書き出し | 端末に溜まったまま渡らない |
| 使用状況へのアクセス | アプリ利用の記録 | アプリ利用が記録されない |
| 通知へのアクセス | 通知・再生情報 | 通知と再生が取れない |
| ユーザー補助 | 前面アプリの把握 | Chrome 履歴の照合ができない |
| 位置情報 | Wi-Fi の SSID、GPX | SSID が空になり、位置情報も取れない |
| 位置情報を「常に許可」 | 画面が消えている間の GPX | 画面を消すと位置情報が途切れる |
| マイク | 定期録音 | 音声が記録されない |
| バッテリー最適化の対象外 | 常駐 | 収集が止まる |

記録する種類は設定画面のチェックボックスで選べます。既定は
アプリ利用・通知・再生・Wi-Fi・Bluetooth・充電が入り、
Chrome 履歴・スクリーンショット・位置情報・音声が外れています。

**端末の利用（ロック解除・画面消灯）だけは切り替えがありません。**
取り込みがこれを使って利用の区間を組み立てるので、止めると
アプリ利用も再生も区間として閉じられなくなります。

定期的に見に行くものは、間隔も変えられます。

| 見に行くもの | 既定 | 範囲 |
| --- | --- | --- |
| アプリ利用 | 5秒 | 5〜3600秒 |
| 動画・音楽の再生 | 5秒 | 5〜3600秒 |
| Chrome 履歴 | 60秒 | 5〜3600秒 |
| スクリーンショット | 60分 | 1〜1440分 |
| 位置情報 | 60秒 | 10〜3600秒 |
| 音声 | 60分 | 1〜1440分 |
| 生ログの書き出し | 60分 | 1〜1440分 |

通知・Wi-Fi・Bluetooth・充電・端末の利用に間隔はありません。
状態が変わったときに Android から届くものを受けているだけだからです。

アプリ利用・Chrome 履歴・書き出しは、間隔を空けても取りこぼしません。
変わるのは生ログに載るまでの遅れだけです。

**再生の間隔だけは記録の粒度そのものです。** 再生時間は見に行った時点どうしの
差で積み上げるので、空けるほど再生の始まりと終わりが粗くなります。
電池のために空けるなら、そのぶん再生時間がずれることを承知で。

ユーザー補助は前面に来たアプリの名前だけを見ます。画面の内容は読み取りません。

全ファイルアクセスだけは他に手段がありません。
生ログの渡し先が Termux の `autolog` と共有する場所で、アプリ専用領域では渡せないためです。

「収集を開始」で常駐が始まります。書き出しは常駐サービスが1時間おき、
WorkManager が15分おき（Android の最短周期）に行います。
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

### 音声 (Android)

「音声を定期的に録る」をオンにすると、`/sdcard/gkill_autolog/audio/` へ
`<端末名>_2026-08-23_12-00-00.m4a` の形で置きます。**マイクの許可が要ります**
（root は要りません）。

既定は60分ごとに1分で、間隔は1〜1440分、長さは1〜60分の範囲で変えられます。
単位はスクリーンショットの撮影間隔と同じです。録音時刻は間隔で丸めるので、
60 なら毎時00分、30 なら毎時00分と30分に録り始めます。

**長さは間隔より短くなります。** 同じ長さを入れると間隔−1分まで丸めて画面へ返します。
同じにすると、次の区切りが来た時点でまだ前の録音が終わっておらず、
その区切りが飛びます（60分ごとに60分と入れると半分しか録れません）。

**始めた直後と間隔を変えた直後は、いま入っている区切りを飛ばします。**
その区切りは頭から録れていないためで、次の区切りの頭から録り始めます。
12:47 に始めたものを「12:00 の録音」として置かないための決まりです。

AAC のモノラル 16kHz 32kbps で、1分あたり約 240KB です。
既定（1時間ごとに1分）なら1日あたり約 6MB になります。

既定では**画面が消えている間とロック中も録ります。** スクリーンショットと違い、
画面が消えていても記録すべき音があるためです。設定を外すと、
端末を使っている間だけになります。

**撮り逃した区切りを後から録り直すことはしません。** スクリーンショットの
「撮り逃したら次に画面を点けたときに撮る」に当たるものは入れていません。
後から録った音は別の時刻の音であって、その区切りの音ではないためです。

無音でもファイルは残します。塞がれたマイクと静かな部屋は同じ無音になり、
区別できないためです。録音の全体が無音だったときは
`adb logcat -s AutologAudio` に警告が出ます。

#### 端末を再起動すると、アプリを開くまで音声だけ止まります

**これは Android の制約で、直せません。** マイクを使う常駐は、アプリが
前に出ている間しか始められず、端末の再起動からの開始は禁止されています。
位置情報の「常に許可」に当たる権限がマイクには無いためです。

ほかの記録（アプリ利用・通知・Wi-Fi・Bluetooth・充電・スクリーンショット・
位置情報）は再起動後も自動で戻ります。**音声だけが止まります。**

アプリを開けば戻ります。設定画面の状態表示に
`音声: 休止（アプリを開くと戻ります）` と出るので、そこで気づけます。
戻っていれば `音声: 録音中（最後に録れたのは 08-23 12:00）` になります。

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

まず手で確かめます。

```sh
autolog import --dry-run --until-now
```

`--dry-run` は gkill を呼ばないので、この確認だけならサーバは動いていなくても
構いません（書き込む予定の内容が出ます）。実際に取り込むときは、
その端末の `gkill_server` が動いている必要があります。

普段は次の2行を1つのスクリプトにして、定期実行から呼びます。
**このスクリプトはリポジトリに入っていません。** 起動の仕組み（Tasker、cron、
Termux:Boot など）は環境によって違うので、置き場所と呼び方は各自で決めてください。

```sh
#!/data/data/com.termux/files/usr/bin/sh
# 取り込みの前に収集アプリへ書き出しをさせる。
# これが無いと、アプリ内に溜まっている直近の記録が共有ディレクトリに出ない。
am broadcast --user 0 -f 0x20 -n com.mt3hr.gkill_autolog/.ExportReceiver

# 上限は「いま」ではなく2分前。接続系のマージ窓 (最長1分) を確実に超えるため。
# オフセットは %z (+0900) ではなく %:z (+09:00)。前者は RFC3339 として解釈できない。
autolog import --cutoff "$(date -d '2 minutes ago' +%Y-%m-%dT%H:%M:%S%:z)"
```

`date -d` が使えない環境では `--until-now` でも動きますが、
切断直後に走ると接続系の区間が割れることがあります。

取り込みの時点で**まだ終わっていない区間**（使用中のアプリ、再生中の音楽など）は、
このときの取り込みには入りません。終わってから次の書き出しで届くと
`start_time` が処理カーソルより過去になりますが、低水位マークが残るので
次の取り込みが自動で遡って拾います。取りこぼしを気にして
取り込みの間隔を詰める必要はありません。

### 5. 同期スクリプトへの組み込み

端末名を決め打ちしないので、どの端末でも同じ内容で動きます。

```sh
# 端末別ユーザーの rep を運ぶ
auto_device=$(basename "$(gkill_server dvnf get)")
auto_user="myuser_auto_$auto_device"
for auto_db in TimeIs URLog Kmemo Tag; do
  auto_src="$HOME/gkill/datas/$auto_user/$auto_db.db"
  if [ -f "$auto_src" ]; then
    gkill_server dvnf copy -f "$auto_src" "Auto$auto_db.db"
  fi
done

# スクリーンショット
gkill_server dvnf move "$HOME/storage/shared/gkill_autolog/screenshots/*.webp" AutoScreenshot
gkill_server idf $(gkill_server dvnf get AutoScreenshot)

# 音声
gkill_server dvnf move "$HOME/storage/shared/gkill_autolog/audio/*.m4a" AutoAudio
gkill_server idf $(gkill_server dvnf get AutoAudio)

# 位置情報。gkill 側の rep 登録は既存の $HOME/Kyou/GPSLogs_* のままでよい。
gkill_server dvnf copy "$HOME/storage/shared/gkill_autolog/gpslog/*.gpx" GPSLogs
```

**どれも拡張子まで指定してください。** 共有ストレージへ出すときは
いったん `<最終名>.tmp` へ書いてから名前を変えていますが、
`*` だとその `.tmp` 自身にも当たります。dvnf の `--ignore` は
ファイル名の完全一致（`.gkill` や `Thumbs.db` など）なので、`.tmp` は素通りします。

位置情報では実際に起きました。gkill は `Contains(".gpx")` で拾うので
`.gpx.tmp` も読みに行き、パースに失敗するとその rep の GPS ログが
丸ごと返らなくなります。0 バイトの `.gpx.tmp` が運ばれて、そうなっていました。

スクリーンショットは `move` なので、掴まれるとアプリ側の名前の変更が失敗して
**その1枚が失われます。** 壊れた `.webp.tmp` は IDF が Kyou として登録します。

`AutoAudio` を足すには、gkill 側に `$HOME/Kyou/AutoAudio_*` の
directory リポジトリを登録しておきます（読み取り専用。上の rep の表を参照）。
`.m4a` は gkill が音声として扱うので、Kyou に再生プレイヤーが付きます。

### root が無い端末

スクリーンショットと Chrome 履歴は取れません。
アプリ利用・通知・Wi-Fi・充電・端末利用・位置情報・音声は取れます。

## 端末を増やすとき

1. その端末の gkill に `<接頭辞><端末名>` のユーザーを作る
2. その端末の `config.env` に `AUTOLOG_DEVICE=<端末名>` を書く
3. `autolog` と（Android なら）アプリを入れる

同期スクリプトは変更しなくて構いません。端末名は `dvnf get` から取ります。

## 何を記録するか変えたいとき

### URL を除外する

`$AUTOLOG_HOME/url_denylist.txt` に書きます。1行1パターンです。

- そのまま書くと部分一致（大文字小文字は区別しない）
- `re:` で始めると正規表現。こちらは大文字小文字を区別するので、
  無視したいときは `(?i)` を先頭に付けます

初回の実行時に既定の内容で作られます。

### 通知を除外する

`$AUTOLOG_HOME/notification_denylist.txt` に書きます。書式は URL 側と同じです。

照合の相手はパッケージ名・アプリ名・**通知チャンネルID**の3つで、
どれかに当たればその通知は gkill へ書き込まれません。

チャンネルIDはアプリが通知の種類ごとに付けている名前です。
同じアプリの通知でも一部だけ落としたいときに使います。
Chrome のダウンロード完了だけを落として他の Chrome の通知は残す、
といった書き分けはこれで行います。

実際の値は生ログで確かめられます。

```sql
SELECT json_extract(payload,'$.package_name'),
       json_extract(payload,'$.channel_id'),
       json_extract(payload,'$.category'),
       json_extract(payload,'$.title')
  FROM raw_event WHERE event_type='notification'
 GROUP BY 1,2,3;
```

生ログに出るのは実際に届いた通知の分だけです。
アプリが持っているチャンネルを網羅して見たいときは端末側で調べます。

```sh
adb shell dumpsys notification --noredact
```

`AppSettings: <パッケージ名>` の下に `NotificationChannel{mId='...'}` が並びます。

チャンネルIDは `downloads` のような短い語なので、部分一致では書かず
`re:(?i)^downloads$` のように前後を留めた正規表現で書いてください。
部分一致だとアプリ名やパッケージ名の一部にも当たります。

前後を留めるぶん、**似た名前の別チャンネルは当たりません**。
Chrome はダウンロードのチャンネルを2つ持っていて、進行中が `downloads`、
「ダウンロードが完了しました」は `completed_downloads` です。
残る意味がないのは後者なので、既定では
`re:(?i)^(completed_)?downloads$` と書いて両方に当てています。

自動化ツールの通知とダウンロードの通知は既定で落ちるようにしてあります。

### 既定の除外リストが更新されたとき

除外リストは**ファイルが無いときだけ**既定の内容で作られます。
利用者が書いたものを勝手に書き換えないためで、
autolog を新しくしても既にある `url_denylist.txt` / `notification_denylist.txt` は
そのまま残ります。

新しい既定を取り込むには、端末ごとに次のどちらかをします。

- 自分で書き足した行が無いなら、ファイルを消して `autolog import --dry-run --until-now`
  を1回走らせます。除外リストの読み込みは書き込みより前なので、`--dry-run` でも
  作り直されます。行数は実行結果の「通知の除外パターン: N 個」で確かめられます
- 書き足した行があるなら、新しいセクションだけを手で写します

### 端末利用のタイトル

`AUTOLOG_USAGE_TITLE` で決めます。省略すると「端末利用」です。

## うまくいかないとき

### 取り込みが0件

上限時刻は既定で「直近の午前4時」です。素の `autolog import` を手で実行すると、
その日に集めた分がまるごと対象外になります。

`run_import.ps1 -UntilNow` と、§4 の Android 側の取り込みスクリプトは、
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
`myuser` でログインしていると見えません。PCへ出てくるのは同期スクリプトを回した後です。

### 書き出しを要求できない

Android 側の取り込みスクリプトは、取り込みの前に収集アプリへ書き出しをさせます（§4）。
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

### Wi-Fi の TimeIs が出ない (Windows)

SSID の取得には**位置情報の許可**が要ります。無いと `ERROR_ACCESS_DENIED` になり、
SSID が空のまま扱われるので接続区間そのものが作られません。
「設定」→「プライバシーとセキュリティ」→「位置情報」で、
位置情報サービスと「デスクトップ アプリに位置情報へのアクセスを許可する」を
オンにしてください。

`autolog collect` のログに一度だけ警告が出ます。許可した後は再起動が要ります。

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
