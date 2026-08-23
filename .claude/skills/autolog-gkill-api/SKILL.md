---
name: autolog-gkill-api
description: "gkill の HTTP API へ書き込む側（src/autolog/internal/gkillclient/）の契約。ステータスで打ち切らず本文の errors を見ること、add_urlog はサーバが対象URLを取りに行くので URLogRateLimit で1秒に1件へ抑えタイトルは自分で埋めること、ログインは IP ごと15分10回で一度失敗したらその実行中は再試行しないこと、rep_name は無視され書き分けは端末別ユーザーで行うこと、update_user_reps は全件置換であること、IDF は mtime を記録時刻にすること、Kyou の ID とタグ ID を決定的に導く名前ベース UUID を扱う。src/autolog/internal/gkillclient/client.go・writer.go を編集するとき、gkill へ送る種別を足すとき必読。「取り込みが途中から全部書けなくなる」「ログインできない」の調査でも必読。"
---

# gkill API へ書き込む側の契約

対象: `src/autolog/internal/gkillclient/**`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## gkill 側で踏みやすい落とし穴

- **ステータスで打ち切る前に本文を読む。** gkill は異常時に 4xx/5xx を返すが（ADR-0045）、
  **エラーの中身（`error_code`）は本文の `errors` にしか入っていない**。
  ステータスだけを見ると「HTTP 401」しか分からず、セッション切れなのか権限不足なのか
  判別できない。逆に HTTP 200 でも `errors` に中身が入ることがあるので、両方を見る。
  `client.go` の `post()` は先に本文をデコードしてから両方を見る形になっている。
  ここを崩すと `isAuthError`（再ログイン）も `isRateLimited`（15分待ちの案内）も
  一切効かなくなり、その端末の取り込みが丸ごと止まる
- **`rep_name` は無視される。** 追加ユースケースが書き込み用リポジトリ固定。書き分けは端末別ユーザーで行う
- **ログインは IP ごとに 15 分で 10 回まで。** ユーザー単位ではない。確認スクリプトの連打で本番の取り込みが全滅する。一度失敗したら実行中は再試行しない
- **`add_urlog` はサーバが対象 URL を取得しに行く。** 1秒に1件へ抑え、タイトルは自分で埋める
- **`update_user_reps` は全件置換。** 手で叩かない
- **IDF は mtime を記録時刻にする。** 保存後に撮影時刻へ合わせる

## URLogRateLimit を外さない

```go
// URLogRateLimit は add_urlog の発行間隔。
//
// gkill サーバは add_urlog のたびに対象URLを再取得する
// (handle_add_urlog.go -> FillURLogField -> getBody が無条件実行)。
// 連続して投入するとサーバから大量の外向きフェッチが出るため間隔を空ける。
const URLogRateLimit = time.Second
```

## ログイン失敗はその実行中は再試行しない

`Client.loginErr` は直近のログイン失敗を覚える。

> 一度失敗したらこの実行中は再試行しない。提案ごとに再ログインすると、
> ログイン試行の回数制限 (IP ごとに 15 分で 10 回) をさらに使い潰し、
> 復帰を遅らせるだけになる。書けなかった分は台帳へ記録されないので、
> 次回の取り込みでやり直される。

回数制限は `codeLoginRateLimit = "ERR000374"` で判定する
（gkill 本体 `gkill_server_api_rate_limit.go` の limit/window）。
認証エラーコードは `ERR000002` / `ERR000013` / `ERR000238` / `ERR000373` の4つで、
**これらが返ったときだけセッションを取り直す**。セッション切れは**1度だけ**ログインし直して再試行する。

`errorBodyLimit = 512` —— HTTPS のサーバへ HTTP で繋いだときのように JSON ではない応答が返ることがある。
原因が分かる程度には見せたいが、丸ごとログへ流し込みたくはない。

## Kyou の ID は提案から決定的に導く

```go
// kyouIDFor は提案から Kyou の ID を決定的に導く。
//
// gkill の各表は ID に一意制約が無い追記型で、読み出しは UPDATE_TIME の
// 最新版を採用する (gkill 本体の LatestDataRepositoryAddress)。
// 同じ id の再追加は「最新版で上書き」なので、書き込みは成功したのに
// 応答を受け取れなかった・台帳への記録前に落ちた、という再試行でも
// 同じ Kyou がもう1つ増えることはない。
```

`tagIDFor` は提案とタグ名からタグ行の ID を決定的に導く（再試行で同じタグが2行になるのを防ぐ）。
どちらも `deterministicKyouID` = 名前ベース (SHA-1) の UUID。

**冪等性はこれだけでは足りない。** 台帳との二段構えである理由は
[autolog-pipeline](../autolog-pipeline/SKILL.md)。

## 端末ごとに別の接続を持つ

`ClientResolver` は端末に対応する接続を返す。**端末ごとに別の gkill ユーザーへ書き分けるため、
接続も端末ごとに要る**（`rep_name` が効かないことの裏返し）。

## エラーメッセージの言語

利用者に見える経路（`config`・`cmd`・`gkillclient`・`inbox`）は日本語。
内部診断（`winapi`・`rawlog` の低層など）は英語のままでよい。

## 関連スキル

- [autolog-pipeline](../autolog-pipeline/SKILL.md) — 台帳・提案・カーソル（書き込みの前段）
- [autolog-powershell](../autolog-powershell/SKILL.md) — `check_connection.ps1` の連打がログイン枠を焼く
- [autolog-windows-collect](../autolog-windows-collect/SKILL.md) — スクリーンショットの mtime（IDF 対策）
