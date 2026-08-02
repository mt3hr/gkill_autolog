# シーケンス図

主要な処理の流れです。用語は [glossary.md](glossary.md) を参照してください。

## 1. PC で集めて取り込む

```mermaid
sequenceDiagram
    participant U as 利用者
    participant C as autolog collect
    participant E as Chrome 拡張
    participant R as raw.db
    participant I as autolog import
    participant G as gkill

    Note over C: ログオン時に常駐開始
    U->>C: ウィンドウ操作・ロック解除など
    C->>R: 生ログを追記
    E->>C: POST /ingest (閲覧・再生)
    C->>R: 生ログを追記

    Note over I: 同期スクリプトまたは手動で実行
    I->>R: カーソル〜上限時刻を読む
    I->>I: normalize で提案を作る
    I->>I: 台帳にある分を除く
    I->>G: POST /api/login
    G-->>I: session_id
    loop 提案ごと
        I->>G: add_timeis / add_urlog / add_kmemo
        G-->>I: 応答 (errors を必ず見る)
        I->>G: add_tag (収集元)
        I->>I: 台帳へ記録
    end
    I->>R: 処理カーソルを進める
```

## 2. Android で集めて取り込む

収集アプリと `autolog` は別のアプリなので、共有ストレージ経由で受け渡します。

```mermaid
sequenceDiagram
    participant A as 収集アプリ
    participant S as /sdcard/gkill_autolog
    participant T as autolog (Termux)
    participant R as raw.db
    participant G as gkill (Termux)

    Note over A: フォアグラウンドサービスで常駐
    A->>A: アプリ利用・通知・Wi-Fi などを観測
    Note over A: 常駐サービスは1時間おき、WorkManager は15分おき
    A->>S: events/<端末>-<時刻>.jsonl.tmp へ書く
    A->>S: .jsonl へ名前を変える

    Note over T: 取り込みスクリプトを実行
    T->>A: am broadcast .ExportReceiver（完了まで待つ）
    A->>S: 溜まっている分を書き出す
    A-->>T: 件数を返す
    T->>S: *.jsonl を読む
    T->>R: 生ログへ追記（重複は弾かれる）
    T->>S: 読めたファイルを消す
    T->>T: normalize で提案を作る
    T->>G: ログインして書き込む
```

**取り込みの前に書き出しを要求します。** これが無いと、アプリ内に溜まっている
直近の記録が共有ディレクトリに出ないまま取り込みが走ります。

書きかけを読まれないよう `.tmp` に書いてから名前を変えます。
`autolog` は `.jsonl` だけを読みます。

## 3. スクリーンショット

autolog は撮って置くだけで、gkill へは入れません。

```mermaid
sequenceDiagram
    participant C as autolog / 収集アプリ
    participant D as 置き場 (screenshots/)
    participant Y as 同期スクリプト
    participant V as dvnf
    participant G as gkill

    Note over C: 撮影間隔で割った境目（既定は毎時00分）
    C->>C: ロック中・画面オフでないか確認
    C->>C: 全モニターを1枚に撮って WebP へ変換
    C->>D: .tmp へ書いてから rename する
    C->>D: 更新時刻を撮影時刻に合わせる

    Note over Y: 同期のたび
    Y->>V: dvnf move <置き場>/* AutoScreenshot
    V->>V: AutoScreenshot_<端末>_<日付>/ へ移動
    Y->>G: gkill_server idf <そのディレクトリ>
    G->>G: 更新時刻を記録時刻として登録
```

`.tmp` を経由するのは、`idf` が書きかけのファイルを拾わないようにするためです。
更新時刻を撮影時刻に合わせないと、取り込んだ日時で記録されてしまいます。

撮り逃した時間の画像は後から作りません。ただし Android だけは、
撮り逃した最後の1区切りを次に画面が点いた時点で撮ります。
このとき記録するのは**実際に撮れた時刻**で、区切りの時刻ではありません。

## 4. 位置情報 (Android)

生ログを経由しません。GPX が最終形で、`autolog import` は関与しません。

```mermaid
sequenceDiagram
    participant L as LocationCollector
    participant P as GpsPointStore
    participant W as GpxWriter
    participant S as gpslog/
    participant Y as 同期スクリプト
    participant G as gkill

    Note over L: GPS・ネットワーク・FUSED を購読
    L->>L: 粗すぎる点と古い点を捨てる
    L->>P: 記録間隔の窓ごとに、いちばん精度の良い点だけ残す

    Note over W: 1分おき
    W->>P: 点が入っている日をすべて読む
    W->>S: その日の全点から作り直し、.tmp から rename
    W->>P: 7日より古い点を消す

    Note over Y: 同期のたび
    Y->>S: dvnf copy gpslog/* GPSLogs
    S->>G: GPSLogs_<端末>_<日付>/ へ入り、gpslog rep として読まれる
```

毎回作り直すのは、GPX に閉じタグが要るからです。
そのため**この DB を作り直す移行をしてはいけません。**
点を消すと、次の書き出しで当日分の軌跡が短くなって消えます。

## 5. 失敗したときの巻き戻し

```mermaid
sequenceDiagram
    participant I as autolog import
    participant L as ledger.db
    participant R as raw.db
    participant G as gkill

    I->>G: 提案A を書き込む
    G-->>I: OK
    I->>L: 提案A を記録

    I->>G: 提案B を書き込む
    G-->>I: errors あり
    Note over I: 台帳へ記録しない

    I->>G: 提案C を書き込む
    G-->>I: OK
    I->>L: 提案C を記録

    Note over I: カーソルは提案B の手前まで引き戻す
    I->>R: SetCursor(提案B の時刻)

    Note over I: 次回の実行
    I->>R: 提案B の手前から読む
    I->>I: 提案A・C は台帳にあるので除外
    I->>G: 提案B だけ書き込む
```

1件の失敗で全体を止めません。失敗した分だけが次回に回ります。

## 6. ログインに失敗したとき

```mermaid
sequenceDiagram
    participant I as autolog import
    participant G as gkill

    I->>G: POST /api/login
    G-->>I: ERR000374 (試行回数の上限)
    Note over I: 失敗を覚える

    loop 残りの提案
        I->>I: 覚えた失敗をそのまま返す
        Note over I: ログインを試みない
    end

    Note over I: 書けなかった分は台帳に載らないので次回やり直される
```

gkill のログインは IP ごとに 15 分で 10 回までです。
提案ごとに再試行すると、上限をさらに使い潰して復帰を遅らせます。
