# 用語集

gkill_autolog の資料全体で使う語をまとめます。
gkill 本体の用語は [gkill の用語集](https://github.com/mt3hr/gkill/blob/main/documents/reverse/glossary.md) を参照してください。

## 中心となる語

### 生ログ (raw log / raw event)

収集プログラムが観測した事実そのもの。加工前の値を保持します。

追記専用の SQLite (`raw.db`) に貯めます。**消しません。**
取り込みの規則を変えたときに、過去に遡って作り直せるようにするためです。

`(端末名, event_id)` で一意。同じイベントを二度入れても増えないので、
再送や再取り込みで壊れません。

スキーマは [`src/autolog/schema/event.schema.json`](../../src/autolog/schema/event.schema.json) にあります。

### 提案 (proposal)

生ログから作った「gkill へこう書き込む」という案。

`normalize` が決定的なルールだけで作ります。同じ生ログからは常に同じ提案が出ます。
提案の ID は元になった生ログの ID から計算するので、
何度作り直しても同じ値になり、台帳での重複判定に使えます。

### 台帳 (ledger)

「この提案はもう書き込んだ」という記録 (`ledger.db`)。

`提案ID → Kyou の ID` を持ちます。
**書き込みに成功した分だけ**記録するので、失敗した分は次回やり直されます。

### 処理カーソル (cursor)

「ここまでは処理した」という位置。

書き込みに失敗した提案があれば、その手前まで引き戻します。
継続中のウィンドウ操作・端末利用セッションがあれば、その開始時刻より先へは進めません。

**引き戻せるのはこの2つだけです。** 接続状態（Wi-Fi・Bluetooth・充電）や
アプリ利用・メディア再生の継続中区間では引き戻しません。
常時つないだままの機器があるとカーソルが永久に止まるためで、
代わりに区間そのものを [持ち越し開区間](#持ち越し開区間-open_state_interval) として保存します。

### 持ち越し開区間 (open_state_interval)

上限時刻の時点でまだ終わりが決まっていない区間。`raw.db` に保存し、次回へ渡します。

次回の区間組み立ての起点として使うため、開始イベントが処理範囲の外に出ていても
正しい開始時刻の区間になります。

終わり方によって2通りあります。

- **接続状態（Wi-Fi・Bluetooth・充電）は終了時刻を持ちません。**
  まだ切断を観測していない、つまり終わりが本当に分かっていない区間です
- **アプリ利用とメディア再生は暫定の終了時刻を持ちます。**
  終わりは観測できていますが、結合の窓の中にいるのでまだ延びるかもしれない区間です

### 通知の既読記録 (notification_seen)

同じ内容の通知を最後に記録した時刻。`raw.db` に端末と内容のハッシュで保存します。

短時間の再通知を1件にまとめる判定を、バッチの切れ目をまたいで効かせるために持ちます。
これが無いと、切れ目をまたいだ再通知だけが重複して残ります。

更新するのは実際に記録した通知だけです。除外された通知では更新しません。

### 収集元 (source)

どの観測から来たかを表す区分。書き込む Kyou にタグとして付きます。

**1つの Kyou に付くタグはこの1つだけです。** 他のタグは付きません。

| タグ | Kyou | 何を観測したか |
| --- | --- | --- |
| `autolog_device` | TimeIs | 端末そのものの利用（ロック解除〜ロック） |
| `autolog_window` | TimeIs | 前面ウィンドウ（Windows）／前面アプリ（Android） |
| `autolog_browser` | URLog | ブラウザでのページ閲覧 |
| `autolog_media` | TimeIs | 動画・音楽の再生（常に作る） |
| `autolog_media` | URLog | 動画・音楽の再生（URL が分かるときだけ追加で作る） |
| `autolog_wifi` | TimeIs | Wi-Fi の接続 |
| `autolog_bluetooth` | TimeIs | Bluetooth 機器の接続 |
| `autolog_charge` | TimeIs | 充電 |
| `autolog_notification` | Kmemo | 通知 |

Dnote で絞るときは、タグだけでは URLog と TimeIs を区別できない行があるため、
`TagEqualPredicate` と `DataTypePrefixPredicate` を組み合わせます。

**再生元のサービスやアプリは区別しません。** YouTube も YouTube Music も
他のサイト・アプリも、すべて `autolog_media` です。
サービス別のタグは付けないので、Dnote で動画と音楽を分けて集計することはできません。

**既に書き込んだ Kyou に後からタグは付きません。** 台帳が「書き込み済み」として
飛ばすため、タグを増やしても過去分は再取り込みでも埋まりません。

### 端末名 (device)

どの端末で観測したかを表す名前。gkill の端末名と揃えます。

`<名前>_<端末名>_<日付>` という形のディレクトリ名に使うため、
**アンダースコアと空白は使えません。**

### 端末別ユーザー

端末ごとに用意する gkill のユーザー (`<接頭辞><端末名>`)。

gkill の追加 API は書き込み先リポジトリを指定できないため、
端末ごとに書き分けるにはユーザーを分けるしかありません。
詳しくは [design-philosophy.md](design-philosophy.md) を参照してください。

### GPX

位置情報の記録。日別に `YYYYMMDD.gpx` として書く。

**ファイル名はこの形でなければならない。** gkill は日付からこの名前を
組み立てて探すため、違う名前だと見つけられない
（`gps_log_repository_gpx_dir_impl.go` の `findGPXFileByDate`）。
日付はローカル日付で、中の時刻は UTC。

生ログ (raw.db) には入れない。GPX が最終形で、`autolog import` は関与しない。

### 受け口 (inbox)

Android で収集アプリと `autolog` が生ログを受け渡す場所 (`/sdcard/gkill_autolog/events/`)。

追記専用の JSONL を置きます。SQLite は置きません（理由は design-philosophy）。

### 除外リスト (deny list)

URLog にしない URL のパターンを書くファイル (`url_denylist.txt`) と、
Kmemo にしない通知のパターンを書くファイル (`notification_denylist.txt`)。
通知側はパッケージ名・アプリ名・通知チャンネルIDを照合します。

何を残すかの判断はすべてこのファイルで決まります。
判断を人にも AI にも都度求めません。

## Kyou の種類との対応

| 観測したこと | gkill での記録 |
| --- | --- |
| 端末の利用、ウィンドウ操作、アプリ利用、Wi-Fi、Bluetooth、充電 | TimeIs（時間区間） |
| 動画・音楽の再生 | TimeIs（曲名・動画名を Title に） |
| ページ閲覧、動画・音楽の再生（URLが分かるとき） | URLog（ブックマーク） |
| 通知 | Kmemo（テキスト） |
| スクリーンショット | IDF（ファイル） |
| 位置情報 | GPX（gpslog rep として読まれる。Kyou にはならない） |
