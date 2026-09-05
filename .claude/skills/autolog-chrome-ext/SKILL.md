---
name: autolog-chrome-ext
description: "Chrome 拡張（src/chrome_ext/、Manifest V3）と受け口（src/autolog/internal/ingest/）の約束。chrome.storage の get から set は原子的でないので Promise チェーンで直列化すること、ロックの中から withState を呼ぶとデッドロックするので入口だけでロックを取ること（例外は removeFromQueue）、イベントを積んでから区間を消す順序、4xx は件数を半分に絞って原因を追い込み1件まで絞れたら捨てて前進すること、再生の区間を一時停止でまたがせず広告では切らないこと、受け口は 127.0.0.1 のみに bind し Bearer トークンを要求し本文上限 8MB であることを扱う。src/chrome_ext/background.js・content_media.js・shared.js・src/autolog/internal/ingest/server.go を編集するとき必読。「閲覧が送られてこない」「キューが減らない」「一時停止しているのに再生の TimeIs が伸びる」の調査でも必読。"
---

# Chrome 拡張と受け口の不変条件

対象: `src/chrome_ext/**` / `src/autolog/internal/ingest/**`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## 状態は Promise チェーンで直列化する。ロックの中から withState を呼ばない

`src/chrome_ext/background.js` の「直列化」節が正本。

> `chrome.storage` の読み書きは get → 変更 → set の3手で、原子的ではない。
> イベントリスナ (タブ閉鎖・メッセージ・アラーム) は await の切れ目で交錯するため、
> **2つの処理が同じスナップショットを読むと、後勝ちで片方の変更が黙って消える。**
> 再生中のタブを閉じると閲覧の確定 (onRemoved) と再生の確定 (pagehide の報告) が
> ほぼ同時に走るので、現実に踏む。
>
> Service Worker は単一インスタンスなので、状態を触る処理をメモリ上の
> Promise チェーンで1列に並べれば足りる。SW が止まればチェーンごと消えるが、
> そのとき実行中の処理も一緒に止まるので取り残しは起きない。
>
> **ロックの中から `withState` を呼ぶとデッドロックする。** 状態を触る関数
> (`syncView` / `closeView` / `recordProgress` / `finalizePlays` / `enqueue`) は
> ロックを取らず、入口 (イベントリスナ) だけで取る。
> **例外は `removeFromQueue`** で、ロックの外で走る flush から呼ばれるため自分で取る。

## イベントを積んでから区間を消す

> イベントを積んでから区間を消す。**逆順だと、消してから積むまでの間に
> SW が落ちたとき区間ごと失われる。** この順なら、積んだ直後に落ちても
> 次の `closeView` が同じ決定的 event_id を積み直すだけで、
> 受け口の `(device, event_id)` が1件に畳む。

決定的 event_id は `browser_view:${view.tabId}:${view.startedAt}`。
`syncView` は複数のイベント (タブ切替・URL遷移・心拍) から並行に呼ばれることがあり、
同じ区間を2回積んでも受け口の `(device, event_id)` で1件に畳まれる。

Service Worker が止まっていた場合、**現在時刻は終了時刻として信用できない**
（`now - view.heartbeatAt > STALE_MS` なら `heartbeatAt` を終了時刻にする）。

## 再生の区間は一時停止をまたがない。広告では切らない

`content_media.js` の `tick`。`play.endedAt` は再生中だけ進むので末尾の一時停止は
入らないが、**`play.startedAt` は固定なので途中の長い一時停止は区間に残る。**

- 止まったまま `PAUSE_SPLIT_MS` を超えたら `report(true)` して `play` を捨てる。
  再開後は新しい `playId` の再生になる
- **停止中のページからは計測を始めない。** 開いたまま再生していない動画を起点にすると、
  再生していない時間が丸ごと区間に入る。ticker を止めても `play` / `playing` イベントが
  動かし直すので取りこぼさない
- **広告 (`isAdShowing`) では切らない。** 切ると1本の視聴が広告のたびに分断され、
  **同じ動画の URLog が広告の数だけできる**（30秒未満の広告で対象が消えても待つ、と同じ理由）
- **`PAUSE_SPLIT_MS` は normalize の `WindowMergeWindow`（1分）と同値にすること。**
  短くすると、切った区間を normalize が結合し直して分割が無意味になる。
  Android の `MediaCollector.kt` も同じ値を持つ

分割は壁時計で測るので、`content_media.test.mjs` は `Date.now` を差し替えて検証する。
既定は固定（＝壁時計が進まない前提の既存テストをそのまま生かす）で、
進めたいテストだけが `advanceClock` で動かす。

## 4xx は半分に絞って原因を追い込み、1件まで絞れたら捨てて前進する

> `flushing` は flush の多重実行を防ぐ。キューが長いと1回の flush が
> 次の心拍をまたぐことがある。Service Worker が止まれば flush も一緒に
> 止まるので、メモリ上のフラグで足りる。
>
> flush はキューを先頭から分けて送る。送信できたものだけをキューから外すので、
> 収集側が落ちていても失われない。
>
> 受け口はバッチに1件でも不正なイベントがあると全体を 400 で拒む。
> **そのまま同じバッチを再送し続けると、以後のイベントがすべて詰まる。**
> 4xx のときは件数を半分に絞って原因のイベントを追い込み、1件まで絞れたら
> それを捨てて前へ進む。5xx とネットワークエラーは次の心拍で再試行する。

## 墓標（確定済み再生の記録）の寿命

`FINALIZED_TTL_MS` は確定済み再生の記録 (墓標) を覚えておく長さ。
墓標は「同じ playId の報告が確定後も続いたとき」の差分計算にだけ要る。
長い動画を見続ける1日は十分に覆い、無限には溜めない（24時間）。

## 受け口は 127.0.0.1 限定・Bearer 必須・本文 8MB まで

`Package ingest` の doc コメントが正本。

> `POST /ingest` Chrome 拡張から。**127.0.0.1 のみに bind する。**
> `Authorization: Bearer <token>` を要求し、受け取ったイベントは
> 生ログストアへ冪等に追記する。**同じ event_id の再送は握り潰される。**
>
> 端末をまたぐ受け口は持たない。各端末は自分で集めて自分の gkill へ取り込む。

`maxBodyBytes = 8 << 20` —— Chrome 拡張はまとめて送ってくるが、それでもこの大きさを超えることはない。
`shutdownTimeout = 5 * time.Second` —— 停止時に送信中のリクエストを待つ時間。

## 関連スキル

- [autolog-pipeline](../autolog-pipeline/SKILL.md) — 受け口が書き込む生ログストアと、遅着イベントの扱い
- [autolog-build-release](../autolog-build-release/SKILL.md) — 拡張のテスト（Node 標準ランナー + chrome スタブ）
