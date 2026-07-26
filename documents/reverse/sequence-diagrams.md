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
    Note over A: 1時間おき、または「今すぐ書き出し」
    A->>S: events/<端末>-<時刻>.jsonl.tmp へ書く
    A->>S: .jsonl へ名前を変える

    Note over T: autolog.sh を実行
    T->>S: *.jsonl を読む
    T->>R: 生ログへ追記（重複は弾かれる）
    T->>S: 読めたファイルを消す
    T->>T: normalize で提案を作る
    T->>G: ログインして書き込む
```

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

    Note over C: 毎時00分
    C->>C: 画面が消えていないか確認
    C->>C: 撮影して WebP へ変換
    C->>D: 保存し、更新時刻を撮影時刻に合わせる

    Note over Y: 同期のたび
    Y->>V: dvnf move <置き場>/* AutoScreenshot
    V->>V: AutoScreenshot_<端末>_<日付>/ へ移動
    Y->>G: gkill_server idf <そのディレクトリ>
    G->>G: 更新時刻を記録時刻として登録
```

更新時刻を撮影時刻に合わせないと、取り込んだ日時で記録されてしまいます。

## 4. 失敗したときの巻き戻し

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

## 5. ログインに失敗したとき

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
