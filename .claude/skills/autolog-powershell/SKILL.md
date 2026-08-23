---
name: autolog-powershell
description: "運用スクリプト（src/scripts/）と Windows PowerShell 5.1 との差の約束。.ps1 と設定ファイルは BOM 付き UTF-8 で保存すること（BOM なしは 5.1 が Shift_JIS と誤読して日本語コメントの次の行を巻き込んで消す）、-File 起動では param ブロックの $PSScriptRoot が空になること、native コマンドを 2>&1 したときの終了エラーと子プロセスの UTF-8 出力の扱い、run_import.ps1 の CutoffLagMinutes が cmd_import.go の untilNowCutoffLag と二重持ちであること、確認スクリプトの連打が gkill のログイン枠を焼くことを扱う。src/scripts/ の .ps1 を編集・追加するとき、タスクスケジューラの登録を触るとき必読。「タスクからだけ落ちる」「日本語コメントの次の行が消えた」の調査でも必読。"
---

# PowerShell 運用スクリプトの不変条件

対象: `src/scripts/**`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## 実行環境は Windows PowerShell 5.1

**ユーザーの実行環境は Windows PowerShell 5.1。** tool の PowerShell は 7 なので再現しない。
切り分けは `powershell.exe -NoProfile -File ...` で行う。

- **BOM なし UTF-8 は行が消える。** 5.1 が Shift_JIS として読み、日本語コメントの2バイト目が改行を飲み込んで次の行を巻き込む。`.ps1` も設定ファイルも BOM 付きで保存する
- **`-File` 起動では param ブロックの `$PSScriptRoot` が空。** 既定値は本体で解決する
- **`2>&1` した native コマンドの stderr が終了エラーになる。** 呼び出しの間だけ `$ErrorActionPreference='Continue'` にし、終了コードで判定する
- **子プロセスの UTF-8 出力が化ける。** 呼び出しの間だけ `[Console]::OutputEncoding` を UTF-8 にする

編集後は BOM が残っているかを必ず確かめる（`file src/scripts/run_import.ps1` が
`UTF-8 (with BOM) text, with CRLF line terminators` のままであること）。
エディタやツールによっては保存時に BOM を落とす。

## 改行コードは `.gitattributes` が固定している

`*.ps1 text eol=crlf` / `*.sh`・`gradlew` は `text eol=lf`。
崩すと 5.1 の実行や `gradlew` の実行が壊れる。詳細は
[autolog-build-release](../autolog-build-release/SKILL.md)。

## 「2分」は Go 側と二重に持っている

`run_import.ps1` の `$CutoffLagMinutes`:

> 最長のマージ窓 (Bluetooth とウィンドウの1分) を確実に超える値。
> 直近 `$CutoffLagMinutes` 分は次回にまわるだけで失われない。

これは `cmd/autolog/cmd_import.go` の `untilNowCutoffLag`（2分）と**同じ値を二重に持っている**。
**片方だけ変えると結合待ちの窓がずれる。** 正本の説明は
[autolog-pipeline](../autolog-pipeline/SKILL.md)。

## 確認スクリプトの連打で本番の取り込みが全滅する

gkill のログインは **IP ごとに 15 分で 10 回まで**（ユーザー単位ではない）。
`check_connection.ps1` / `setup_auto_users.ps1` には明示的なガードとメッセージが入っている。
外さないこと。詳細は [autolog-gkill-api](../autolog-gkill-api/SKILL.md)。

## タスクスケジューラの登録

`register_tasks.ps1` はログオン時 `collect` / 毎日4時 `import` を登録する。
**「窓を消したい」という理由で設定を変えてはいけない**（同ファイルにコメントで理由がある）。
セッション0で動くようになると入力デスクトップを開けず、スクリーンショットが撮れなくなる。

## 関連スキル

- [autolog-gkill-api](../autolog-gkill-api/SKILL.md) — ログイン枠と `errors` の見方
- [autolog-pipeline](../autolog-pipeline/SKILL.md) — `untilNowCutoffLag` の意味
- [autolog-build-release](../autolog-build-release/SKILL.md) — `.gitattributes` の改行固定
