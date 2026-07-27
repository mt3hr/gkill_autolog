# 実装仕様

パッケージの責務とデータの流れをまとめます。
なぜそうしたかは [design-philosophy.md](design-philosophy.md) を参照してください。

## データの流れ

```
[収集]                    [保管]              [整理]           [書き込み]
Windows collect ──┐
Chrome 拡張 ──────┼──→  raw.db  ──→  normalize  ──→  gkill HTTP API
Android アプリ ───┘    (追記専用)      (提案)         (端末別ユーザー)
   └→ JSONL → inbox                                      │
                                                    ledger.db
                                                    (書き込み済み)
```

スクリーンショットと位置情報は別経路です。ファイルとして置くところまでを担当し、
gkill へ入れるのは同期スクリプトの役目です。

## サブコマンド

| コマンド | 何をするか |
| --- | --- |
| `autolog collect` | 常駐して集める。Chrome 拡張の受け口も開く |
| `autolog import` | 生ログを整理して gkill へ取り込む |
| `autolog screenshot` | スクリーンショットを1枚撮る |
| `autolog status` | 生ログの溜まり具合と処理カーソルを表示する |

### autolog import の流れ

1. 共有ディレクトリの JSONL を生ログへ取り込む（Android のみ。`inbox`）
2. 処理カーソル 〜 上限時刻の生ログを読む
3. `normalize` で提案を作る
4. 台帳にある提案を除く
5. 端末ごとに gkill へログインして書き込む
6. 書き込めた分を台帳へ記録する
7. 処理カーソルを進める（失敗した提案の手前まで引き戻す）

`--dry-run` を付けると 5〜7 を行いません。受け口の JSONL も消しません。

## internal/rawlog — 生ログ

SQLite。生ログ本体は追記専用です。

```sql
CREATE TABLE raw_event (
  schema_version INTEGER NOT NULL,
  event_id       TEXT NOT NULL,   -- 端末内で一意
  device         TEXT NOT NULL,
  event_type     TEXT NOT NULL,
  start_time     TEXT NOT NULL,   -- RFC3339
  end_time       TEXT,
  captured_at    TEXT NOT NULL,
  payload        TEXT NOT NULL    -- JSON。加工前の値
);
CREATE UNIQUE INDEX idx_raw_event_id ON raw_event(device, event_id);
```

処理の途中状態を持つテーブルが2つあります。どちらも生ログではないので上書きします。

```sql
-- どこまで処理したか
CREATE TABLE process_cursor (
  name       TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- まだ切断を観測していない接続区間。次回へ持ち越す
CREATE TABLE open_state_interval (
  device     TEXT NOT NULL,
  source     TEXT NOT NULL,
  state_key  TEXT NOT NULL,   -- SSID や機器名
  title      TEXT NOT NULL,
  start_time TEXT NOT NULL,
  event_ids  TEXT NOT NULL,   -- JSON 配列
  PRIMARY KEY (device, source, state_key)
);
```

### 時刻は必ずローカルへ揃えてから保存する

`storageTime()` が保存直前に `time.Local` へ変換します。

時刻を文字列で持ち `ORDER BY start_time` で並べるため、
オフセットが混ざると辞書順が時刻順と一致しなくなります。
Chrome 拡張や Android は UTC (末尾 Z) で送ってくることがあるので、
入口ではなく保存の直前で揃えます。

### イベントの種類

| event_type | 内容 |
| --- | --- |
| `input` | クリック・ホイール・キー入力を伴う前面ウィンドウ。マウス移動では出さない |
| `session` | 端末の利用状態の変化（ロック解除、ロック、電源） |
| `wifi` | Wi-Fi の接続・切断。SSID のみ。BSSID は持たない |
| `bluetooth` | Bluetooth 機器の接続・切断 |
| `power` | 充電の開始・終了。残量は持たない |
| `browser_view` | ブラウザでのページ表示 |
| `media_play` | 動画・音楽の再生（実再生時間つき） |
| `app_usage` | アプリの利用 |
| `notification` | 通知 |

## internal/normalize — 整理

生ログを提案へ変換します。**ここが本システムの中核**です。

外部に依存しない純粋な処理なので、入力を並べれば結果が決まります。
境界値の単体テストを厚く書いてあります。

### 閾値

要件で決めた値です。根拠は [requirements.md](requirements.md) にあります。

| 定数 | 値 | 用途 |
| --- | --- | --- |
| `IdleTimeout` | 5分 | これだけ入力が無ければウィンドウのセッションを終える |
| `WindowMergeWindow` | 1分 | この時間内に元のウィンドウへ戻ったら前後を結合する |
| `MinWindowDuration` | 1分 | これ未満のウィンドウ操作は記録しない |
| `MinViewDuration` | 30秒 | これ未満の閲覧は記録しない |
| `MinPlayedSeconds` | 30秒 | これ未満しか再生していないものは記録しない |
| `MinAppUsage` | 30秒 | これ未満のアプリ利用は記録しない |
| `WifiMergeWindow` | 30秒 | この時間内の再接続を結合する |
| `BluetoothMergeWindow` | 1分 | 同上 |
| `ChargeMergeWindow` | 30秒 | 同上 |
| `NotificationDedupeWindow` | 5分 | この時間内の同内容の再通知を1件にまとめる |

### セッションの終わりは「最後の入力時刻」

5分無入力でセッションを終えますが、終了時刻は**アイドルと判定した時刻ではなく
最後に入力があった時刻**です。触っていない5分間を記録に含めません。

### 継続中のセッションは持ち越す

上限時刻の時点でまだ終わっていないセッションは提案にしません。次回まとめて処理します。

持ち越し方は2通りあり、対象によって使い分けます。

**ウィンドウ操作と端末利用**は、処理カーソルをその開始時刻まで引き戻します。
開いているのは常に「いま操作中の1件」なので、引き戻す幅は高々直近の数分です。

**接続状態（Wi-Fi・Bluetooth・充電）**は、カーソルを引き戻さず、
開いている区間そのものを `raw.db` の `open_state_interval` へ保存して次回へ渡します。
こちらを引き戻し方式にできないのは、**切断しない機器があると永久に止まるから**です。
スマートウォッチのように常時つないだままの機器があると区間が閉じることはなく、
カーソルはその接続時刻に固定され続けます。処理する生ログは日ごとに増え、
いずれ現実的な時間で終わらなくなります。

持ち越した区間は次回の区間組み立ての起点として使うので、
**開始イベントが処理範囲の外に出ていても正しく閉じられます**。

### 提案の ID は決定的

`makeID(種別, 収集元, 元イベントID群)` を sha256 で計算します。
同じ生ログからは常に同じ ID が出るので、台帳での重複判定に使えます。

### 動画・音楽はブラウザ経路から除く

動画サイトの URL はメディアとして記録するため、ブラウザ閲覧の経路からは除きます。
判定はホスト名で行います（URL に文字列が含まれるかで判定すると誤爆します）。

## internal/gkillclient — gkill への書き込み

純 Go の `net/http` だけを使います。

| 用途 | エンドポイント |
| --- | --- |
| ログイン | `POST /api/login` |
| TimeIs | `POST /api/add_timeis` |
| URLog | `POST /api/add_urlog` |
| Kmemo | `POST /api/add_kmemo` |
| タグ | `POST /api/add_tag` |

### 応答は必ず中身を見る

gkill は異常時も HTTP 200 を返し、本文の `errors` にエラーを載せます。
ステータスコードだけを見ていると失敗を見落とします。

### 端末名は自分で設定する

エンティティを丸ごと送るので、`create_app` に `gkill_autolog`、
`create_device` に端末名が入ります。

`rep_name` は送っても無視されます（追加ユースケースが書き込み用リポジトリ固定のため）。
書き分けは端末別ユーザーで行います。

### ログインの失敗は繰り返さない

一度失敗したら、その実行中はもう試みません。
gkill のログインは IP ごとに 15 分で 10 回までなので、
提案ごとに再試行すると上限を使い潰して復帰を遅らせます。

書けなかった分は台帳に載らないので、次回やり直されます。

### URLog は1秒に1件

gkill が `add_urlog` のたびに対象 URL を取得しに行くためです。
タイトルは必ず自分で埋めて送ります。

## internal/inbox — Android からの受け渡し

共有ディレクトリの `*.jsonl` を読んで生ログへ入れます。

- `.jsonl` だけを読みます。書きかけの `.jsonl.tmp` は読みません
- 取り込めたファイルは消します。消す前に落ちても、生ログ側が重複を弾くので壊れません
- 1行が壊れていても、他の行は取り込みます
- 端末名が許可されていなければその行だけ捨てます

## internal/ingest — Chrome 拡張の受け口

`POST /ingest` のみ。127.0.0.1 だけに bind します。
`Authorization: Bearer <token>` を要求します。

トークンは初回起動時に生成して `$AUTOLOG_HOME/ingest_token.txt` に保存します。

端末をまたぐ受け口は持ちません。

## internal/config — 設定

優先順位は次のとおりです。

1. 環境変数
2. `$AUTOLOG_HOME/config.env`
3. 共有ディレクトリの `config.env`（Android のみ）
4. 既定値

設定ファイルは KEY=VALUE 形式です。`#` で始まる行と空行は無視します。
先頭の BOM は取り除きます。

### 端末名の既定値

未設定ならこの機械のホスト名を使います。
端末名に使えない文字（アンダースコア、空白、ドット）は落とします。

コードに端末名は書きません。

### タイムゾーン (Android)

`timezone_android.go` がシステム設定からタイムゾーンを読み、`time.Local` に入れます。
`main()` の先頭で、他の処理より先に実行します。

Android 以外では何もしません。

## Android アプリ

### 端末名は config.env が優先

アプリの設定と `config.env` の両方に端末名を持つと食い違います。
`config.env` を正とし、起動時にアプリの設定へ反映します。

### 書き出しは排他する

常駐サービスと WorkManager の両方から呼ばれるため、プロセス内で排他します。
これが無いと、同じ内容の JSONL が二重に書き出されます。

### 外から書き出しをさせられる

`ExportReceiver` を明示的に叩くと、その場で書き出します。
`autolog.sh` が取り込みの直前に使います。これが無いと、
アプリ内に溜まっている直近の記録が共有ディレクトリに出ないまま取り込みが走ります。

```sh
am broadcast --user 0 -f 0x20 -n com.mt3hr.gkill_autolog/.ExportReceiver
```

`am broadcast` は順序付きブロードキャストを送って完了まで待ち、受け口は
`goAsync()` で書き出しが終わるまで完了扱いにしないので、**呼び出し側は完了を待てます**。
結果コードに件数、結果データに人が読める文字列を載せるので、成否を判別できます。

`intent-filter` は付けません。呼ぶ側にコンポーネント名を明示させることで、
他のアプリのブロードキャストにたまたま反応することがなくなります。

### 更新後も収集を続ける

Android はアプリを入れ替えるとプロセスを停止しますが、常駐サービスは自動では戻りません。
`BootReceiver` は端末起動時の `BOOT_COMPLETED` に加えて、
更新完了時に自分自身へだけ届く `MY_PACKAGE_REPLACED` でも収集を開始します。

これが無いと、更新のたびに収集が黙って止まり、
設定画面で「収集を開始」を押すまで記録が途切れます。

### 直前の SSID は永続化する

Wi-Fi の切断時は SSID を取得できないため、直前に接続していた SSID を覚えておいて
切断イベントを作ります。この値はメモリだけで持たず永続化します。

プロセスが死ぬと失われ、復帰後は切断イベントを作れなくなります。
すると接続区間が閉じないまま残り、
[持ち越し開区間](glossary.md#持ち越し開区間-open_state_interval) に溜まり続けます。

### 位置情報 (GPX)

`LocationCollector` が `LocationManager` で GPS とネットワークの両方を購読します。
`FusedLocationProviderClient` は使いません。Google Play 開発者サービスへの
依存を増やさないためです。

屋内では GPS が入らずネットワーク側しか取れないことがあるので両方を購読し、
設定した間隔より短い間隔で来た点は捨てます。

点は `GpsPointStore`（アプリ専用領域の SQLite）に貯めます。
メモリだけで持たないのは、プロセスが落ちたときにその日のそれまでの分を失わないためです。

`GpxWriter` が1分おきに、点が入っている日をすべて書き出します。
当日だけでなく過去日も対象にするのは、日付が変わった直後に前日分を書き残さないためです。

**その日の全点から毎回作り直し、`.tmp` へ書いてから rename します。**
追記にしないのは、GPX に閉じタグが要るからです。書きかけのファイルを
同期スクリプトがコピーすると解釈に失敗し、その rep の GPS ログ全体が読めなくなります。

書いた点は7日残してから消します。書き損じても作り直せるようにするためです。

### スクリーンショットの更新時刻

保存後に更新時刻を撮影時刻へ合わせます。
gkill の IDF は更新時刻を記録時刻として使うためです。

root の `screencap` を使います。root が無い端末では撮れません。
