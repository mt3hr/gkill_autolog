---
name: autolog-windows-collect
description: "Windows の常駐収集（src/autolog/internal/collect/・winapi/・shot/・proclock/）の約束。Wi-Fi の SSID 取得には位置情報の許可が要り無いと ERROR_ACCESS_DENIED で空になること、Bluetooth の接続判定は fConnected ではなく SetupAPI で行うこと、停止時の最後の書き出しはロック待ち3秒（Windows は既定5秒でプロセスを殺す）、キューが詰まったらイベントを捨てて操作を遅くしないこと、スクリーンショットは保存後に mtime を撮影時刻へ合わせること、collect と import の多重起動を防ぐファイルロックを扱う。src/autolog/internal/collect/・internal/winapi/・internal/shot/・internal/proclock/・cmd/autolog/cmd_collect.go を編集するとき必読。「SSID が空になる」「ログオフで最後の記録が消える」の調査でも必読。"
---

# Windows 収集の不変条件

対象: `src/autolog/internal/collect/**` / `internal/winapi/**` / `internal/shot/**` /
`internal/proclock/**` / `cmd/autolog/cmd_collect.go` / `cmd_screenshot.go`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## Windows 収集で踏みやすい落とし穴

- **Wi-Fi の SSID には位置情報の許可が要る。** 無いと `ERROR_ACCESS_DENIED` で空になる
- **Bluetooth の接続判定は `fConnected` では不正確。** SetupAPI を使う

どちらも例外は立たない。SSID は空文字、Bluetooth は BLE の機器（マウス・キーボード）が
一覧から丸ごと落ちる形で現れる。

## 停止時の書き出しはロック待ち3秒

```go
// finalFlushBusyWait は停止時の最後の書き出しに掛けるロック待ちの上限。
//
// コンソールのクローズやログオフでは、Windows が既定5秒でプロセスを殺す。
// そのとき import のトランザクションが raw.db を掴んでいると、
// 通常のロック待ち (10秒) を待っているだけで期限切れになり、最後のイベント
// (collector_stop や直前の入力) が1件も書けずに消える。
// 5秒の枠内に収まる待ちにして、書ける状況なら確実に書く。
const finalFlushBusyWait = 3 * time.Second
```

`finalFlush` は**書けなかった分の持ち越しをしない（失われる）**。これは許容している挙動で、
未終了セッションは次回起動の `recoverUnfinishedSession` が補う。

## キューが詰まったら捨てる

`Emitter.Emit` はイベントを書き込みキューへ入れる。
**キューが詰まっている場合は捨てて警告を出す。操作を遅くしないことを優先する。**
不正なイベントはキューへ流さない —— `PutBatch` はバッチ内に1件でも不正があると全体を失敗させるので、
入口で弾かないと他のイベントまで巻き込む。

## スクリーンショットは保存後に mtime を撮影時刻へ合わせる

```go
// gkill の IDF は登録時のファイル mtime を RelatedTime にするため、
// 保存後に必ず mtime を撮影時刻へ合わせる。
```

autolog がやるのは撮って置くところまで。保存先を gkill の IDF リポジトリとして取り込ませることで
Kyou になる（要件 §10）。

## collect と import の多重起動を防ぐ

`Package proclock` の doc コメントが正本。

> collect と import はどちらも多重起動すると実害がある。
>   - collect: 2個目が `recoverUnfinishedSession` で偽の recovered lock を書き、
>     `collector_start` / `collector_stop` で**稼働中の利用セッションを分断する**
>   - import: Kyou の ID は提案から決定的に導くので単純な二重登録にはならないが、
>     収集と並行すると2つの実行が読む範囲・持ち越しがずれて別 ID の提案が組み上がりうる。
>     ほかにも台帳スナップショットの不整合や、URLog のサーバ取得・
>     ログイン枠 (IP ごとに 15 分で 10 回) の浪費がある
>
> OS のファイルロック (Windows: LockFileEx / それ以外: flock) を使う。
> プロセスが死ねば OS がロックを解放するので、消し忘れたロックファイルが
> 残っていても次の起動を妨げない。**ロックファイル自体は消さない。**

## セッション0で動かさない

`cmd/autolog/console_windows.go` —— セッション0で動くようになると入力デスクトップを開けなくなり、
**スクリーンショットが撮れなくなる**。

## 関連スキル

- [autolog-pipeline](../autolog-pipeline/SKILL.md) — 書き込み先の生ログストアと持ち越し
- [autolog-gkill-api](../autolog-gkill-api/SKILL.md) — IDF が mtime を記録時刻にする話の出どころ
- [autolog-powershell](../autolog-powershell/SKILL.md) — 常駐の起動・タスクスケジューラ登録
