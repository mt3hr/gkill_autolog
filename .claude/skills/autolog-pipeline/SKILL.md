---
name: autolog-pipeline
description: "生ログ→提案→台帳の中核（rawlog / normalize / ledger / inbox / cmd_import.go）の不変条件。低水位マーク CursorBackfill、AdvanceCursorChecked の検査→マーク削除→前進を1トランザクションで行うこと、MaxRowID を Range の前に取ること、makeID に端末名を混ぜる決定的ID、冪等性の二段構え（決定的id＋台帳のイベント包含）、接続区間はカーソルを引き戻さない持ち越し、受け付けなかった行のある JSONL は消さないこと、要件で決まっている閾値とマージ窓を扱う。src/autolog/internal/rawlog/・internal/normalize/・internal/ledger/・internal/inbox/・cmd/autolog/cmd_import.go を編集するとき必読。「取り込んだはずのイベントが二度と入らない」「同じ区間が別idで二重に出る」の調査でも必読。"
---

# 生ログ・整理・台帳の不変条件

対象: `src/autolog/internal/rawlog/**` / `internal/normalize/**` / `internal/ledger/**` /
`internal/inbox/**` / `cmd/autolog/cmd_import.go`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**
多くは「例外もエラーも出さずに静かに壊れる」種類で、破っても目の前ではエラーにならない。

## 生ログは消さない

**生ログは消さない。** 追記専用。`(端末, event_id)` で一意なので再取り込みが安全。
**失敗は記録せず次回やり直す。** 台帳には成功した分だけ載せる。

## 低水位マーク（CursorBackfill）を外さない

`CursorBackfill` は「カーソルより過去の `start_time` を持つイベントが後から挿入された」ことを表す
低水位マーク。挿入されたそのイベントの最小 `start_time` が入る（`internal/rawlog/store.go`）。

> 区間イベントは終わってから届く。Android の使用中アプリや再生中の音楽、
> Chrome 拡張が溜めていた閲覧は、取り込みがカーソルを進めた後に
> カーソルより過去の start_time で届くことがある。マークが無いと
> import は二度とその範囲を読まず、届いたイベントが恒久に取り込まれない。
> import はこのマークまで開始位置を引き戻し、処理し終えたら消す。

## MaxRowID は Range を呼ぶ前に取る

`MaxRowID` は import が「読む範囲をここで確定した」という印に使う。

> Range を呼ぶ前に取ること。後に取ると、読み取りと印の間に挿入されたイベントが
> 「読んだ範囲の中」扱いになり、AdvanceCursorChecked の検査から漏れる。

## AdvanceCursorChecked の3つはひとつのトランザクションで行う

`AdvanceCursorChecked` は、**読み残しの検査・消費した低水位マークの削除・処理カーソルの前進**を、
ひとつのトランザクションで行う。

> import は Range で読んでから書き終えるまでに数分かかる (URLog は1秒に1件)。
> その間に収集プロセスが挿入したイベントのうち、start_time が
> 旧カーソルと新カーソルの間のものは、挿入時の markBackfill (旧カーソル基準)
> の対象にならず、Range のスナップショットにも入っていない。
> そのままカーソルを進めると二度と読まれず恒久に取りこぼす。
>
> そこで afterRowID (Range の前に取った MaxRowID) より後に挿入され、
> start_time が新カーソルより前のイベントを探し、あればその最小 start_time で
> 低水位マークを下げてからカーソルを進める。次回の取り込みが読み直す。
>
> clearMark は今回の取り込みが読み直しに使った低水位マークの値 (無ければゼロ値)。
> マークがその値のままなら消す。**別々の文で消すと「遅着イベントを見つけてマークを残す」と
> 「消費済みマークを消す」が打ち消し合いうる**ので、削除→下げ→前進の順を
> このトランザクションの中で確定させる。

## 接続区間はカーソルを引き戻さない

`OpenStateInterval` は cutoff の時点でまだ確定していない区間。使いみちは2つある。

> 1つ目は接続区間 (Wi-Fi・Bluetooth・充電) で、まだ切断を観測していないもの。
> 常時つないだままの機器 (スマートウォッチや自宅の Wi-Fi) があると、
> その区間は何日も閉じない。開始時刻までカーソルを引き戻していると
> **永久に処理位置が進まなくなる**ので、開いた区間だけをここへ保存して
> 次回へ持ち越し、カーソルは cutoff まで進める。
>
> 2つ目はアプリ利用やメディア再生の末尾で、結合相手が次のバッチに現れうるもの。
> こちらは終了時刻を観測済みなので End に入れる。次のバッチで結合相手が
> 現れなければ、そのまま確定して書き出す。

`normalize.go` の `connectionStates` も同じ理由で持ち越す —— 開いている区間は `Result` で
持ち越して次回に閉じる。**記録をやめる設定にするときに開いている区間を閉じる**のは収集側の責務で、
そちらは [autolog-android](../autolog-android/SKILL.md) が正本。

## 通知の重複排除は取り込み1回では完結しない

`NotificationSeen` は通知の重複排除に使う「直近に記録した内容」1件。

> 同じ内容の再通知を1件にまとめる判定は、取り込み1回のなかだけで完結しない。
> **取り込みを何分おきに走らせても結果が変わらないよう**、直近の記録時刻をここへ残す。

## 冪等性は「決定的 id」と「台帳」の二段構え

`Package ledger` の doc コメントが正本。要点:

> Kyou の id は提案から決定的に導く (`gkillclient.kyouIDFor`) ため、同じ提案の
> 再送は gkill 側では「最新版で上書き」になり増えない。それでも台帳は要る。
> 送信そのものを省くことで URLog のサーバ側フェッチやログイン枠を浪費しないため、
> そしてカーソルの引き戻しで**別の id** の提案が再構成される断片を弾くためで、
> 冪等性は「決定的 id」と「台帳」の二段構えになっている。
>
> 提案 id だけでは足りない。カーソルの引き戻し（継続中セッションや書き込み失敗）で
> 確定済み区間のイベント列を途中から読み直すと、部分集合のイベントから
> **別の id** を持つ提案が再構成されるため、id の突合だけでは素通りしてしまう。
> そこで書き込んだ提案の元イベントも `(kind, source, device, event_id)` で記録し、
> 「元イベントがすべて記録済み」の提案は既に書いた区間の断片とみなして弾く。
>
> 一部だけが記録済みの提案（遅れて届いた生ログが確定済み区間を延ばした形）を
> どう扱うかは収集元によって違うため、台帳は件数 (`CoveredCount`) を返すだけにし、
> **判断は書き込み側 (`gkillclient.Writer`) に置く**。

## makeID に端末名を混ぜる

`makeID` は元になった生ログのイベントIDから決定的な識別子を作る。

> 同じイベントIDが2回入っていても1回として扱う。カーソルの引き戻しで同じイベントを
> 読み直すことがあり、重複を数えていると読み直しの有無で識別子が変わってしまう。
>
> **端末名も入れる。** 生ログの一意性は `(端末, event_id)` なので、別々の端末が
> たまたま同じ event_id (タイムスタンプ由来の決定的な値) を作ると、
> イベントIDだけでは別の観測の提案が同じ識別子になり、台帳の突合で
> **後から来た端末の分が「書き込み済み」として黙って落ちてしまう**。

## 受け付けなかった行がある JSONL は消さない

`internal/inbox/inbox.go` の取り込み。

> 受け付けなかった行が残っている。**ここで消すとその生ログが恒久に失われる**ので、
> ファイルは残す。原因（端末名の設定やスキーマ）を直せば次回の取り込みで残りも入り、
> そのとき消える。取り込み済みの行は `(device, event_id)` の重複として弾かれる。

## 閾値とマージ窓は要件由来。変えると過去分と非互換になる

`internal/normalize/normalize.go` の定数群は要件で決まっている。
`IdleTimeout` 5分（§6.3）/ `WindowMergeWindow` 1分（§6.3）/ `MinWindowDuration` 1分 /
`MinViewDuration` 30秒（§7.1）/ `MinPlayedSeconds` 30（§8.1）/ `MinAppUsage` 1分（§11.2）/
`WifiMergeWindow` 30秒（§9.1）/ `BluetoothMergeWindow` 1分（§9.2）/ `ChargeMergeWindow` 30秒（§9.3）/
`NotificationDedupeWindow` 5分（§13）/ `NotificationUpdateWindow` 30分。

`MinWindowDuration` と `MinAppUsage` を同じ値にしてあるのは意図的
（「一瞬触っただけの操作は残さない」という同じ判断なので、端末の種類で基準が変わる理由がない）。
**判定は割り込みの結合を済ませたあとの長さで行う** —— 短い区間どうしが結合されて下限を超えることがある。

`NotificationUpdateWindow` を無制限にしてはいけない —— Android は通知IDを使い回すので、
処理する窓（通常1日、初回は7日）の中の別々の通知が最終状態だけに潰れ、
**結果が取り込みの間隔に依存してしまう**。

## 「2分」は Go 側と PowerShell 側で二重に持っている

`untilNowCutoffLag`（`cmd/autolog/cmd_import.go`）は `--until-now` のときに「いま」から引く猶予。

> 接続系 (Wi-Fi・Bluetooth・充電) やウィンドウの結合は「次のイベント」を見て
> 初めて判定される。直近ちょうどまで処理すると、切断・切替の直後が
> 結合相手を待たずに確定しうる。最長のマージ窓 (1分) を確実に超える値にし、
> `run_import.ps1` が自前で持つ 2 分と揃える。直近 2 分は次回にまわるだけで失われない。

**片方だけ変えると結合待ちの窓がずれる。** PowerShell 側は `$CutoffLagMinutes`
（[autolog-powershell](../autolog-powershell/SKILL.md)）。

## 一部だけ書き込み済みの区間は書かない

`gkillclient/writer.go` の `Overlapped` 判定。遅れて届いた生ログが確定済みの区間を延ばした形なので、
そのまま書くと既に書いた短い区間と重なる長い区間がもう1本できる。**延びた分の記録は失われるが、
重複した TimeIs を作るよりましと判断する。** 接続系は対象にしない（観測の切れ目マーカーの
セッションイベントを複数の区間が共有するので、一部重複が正常な形）。URLog は元イベントが1つなので
一部重複になりえず、Kmemo (通知) は同じ通知の更新列が正当に重なるので、どちらも対象にしない。

## テスト

`internal/normalize` が最重要。閾値・結合・持ち越しの境界値を検証している。

## 関連スキル

- [autolog-gkill-api](../autolog-gkill-api/SKILL.md) — 書き込み側の契約と判断（`Writer`）
- [autolog-android](../autolog-android/SKILL.md) — 生ログの送り手（JSONL・開いている区間を閉じる）
- [autolog-chrome-ext](../autolog-chrome-ext/SKILL.md) — もう一方の送り手（受け口 `ingest`）
- [autolog-powershell](../autolog-powershell/SKILL.md) — `run_import.ps1` の `$CutoffLagMinutes`
