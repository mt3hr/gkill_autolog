# AGENTS.md

このリポジトリで作業する AI エージェント共通の入口（Codex CLI / Cursor / Claude Code / Gemini CLI / Copilot 等）。
Claude Code は `CLAUDE.md` の import 経由でこのファイルを読み込む。**規約の編集は必ずこのファイル側で行うこと。**

## Project Overview

gkill_autolog は、PC・Android 端末・Chrome から客観的な操作ログを自動収集し、
[gkill](https://github.com/mt3hr/gkill) へ Kyou として記録するツール群。

**各端末が自分で集めて自分の gkill へ取り込む。** 端末をまたぐ転送はしない。
その先の集約は gkill 側の既存の同期に任せる。

Go の CLI（収集・取り込み）、Android の収集アプリ、Chrome 拡張、
PowerShell スクリプトで構成される。MIT ライセンス。

## 触る場所ごとの必読資料（ルーティング表）

下の表で**触るファイルに一致する行があれば、編集を始める前に「先に読む」のスキルファイルを Read すること**。
gkill_autolog の不変条件の多くは「例外もエラーも出さずに静かに壊れる」種類で、読まずに書くと3列目が起きる。
パス連動のスキル機構を持たないエージェントも、これらはただの Markdown なので同じパスを Read すればよい。

<!-- ROUTING-TABLE:BEGIN 手書きの表。スキルの増減時は必ずここも更新する（verify_docs の checkSkills が双方向網羅を検査） -->

| 触るファイル | 先に読む | 読み落とすと |
|---|---|---|
| `src/autolog/internal/rawlog/**`・`internal/normalize/**`・`internal/ledger/**`・`internal/inbox/**`・`cmd/autolog/cmd_import.go` | [autolog-pipeline](.claude/skills/autolog-pipeline/SKILL.md) | 低水位マーク `CursorBackfill` を外すと、後から届いた区間イベント（Android のアプリ利用・再生、拡張が溜めた閲覧）を import が二度と読まず**恒久に取りこぼす**／`AdvanceCursorChecked` の「読み残し検査→マーク削除→カーソル前進」を別々の文へ割ると、遅着の検出と消費済みマークの削除が打ち消し合って同じ取りこぼしが出る／`MaxRowID` を `Range` の後に取ると挿入分が「読んだ範囲の中」扱いになり検査から漏れる／`makeID` から端末名を落とすと、別端末の同じ `event_id` の提案が台帳突合で**エラーも出さず落ちる**／受け付けなかった行がある JSONL を消すと、その生ログが恒久に失われる |
| `src/autolog/internal/gkillclient/**` | [autolog-gkill-api](.claude/skills/autolog-gkill-api/SKILL.md) | ステータスで打ち切って本文の `errors` を読み落とし、再ログインも回数制限の検出も効かなくなる／失敗を成功扱いして台帳へ載せ**二度とやり直されなくなる**／`add_urlog` を1秒1件へ抑えないと、サーバ側が1件ごとに対象URLを取りに行くので大量の外向きフェッチが出る／提案ごとに再ログインすると IP ごと15分10回の枠を使い潰し、その端末の取り込みが全滅する／`update_user_reps` を手で叩くと全件置換で既存 rep が消える |
| `src/autolog/internal/collect/**`・`internal/winapi/**`・`internal/shot/**`・`internal/proclock/**`・`cmd/autolog/cmd_collect.go` | [autolog-windows-collect](.claude/skills/autolog-windows-collect/SKILL.md) | SSID は位置情報の許可が無いと `ERROR_ACCESS_DENIED` を返し**例外も立てず空**になる／Bluetooth を `fConnected` で判定すると BLE の機器（マウス・キーボード）が丸ごと出ない／停止時の書き出しのロック待ちを3秒より延ばすと、Windows が5秒でプロセスを殺すので `collector_stop` と直前の入力が1件も残らない／ロックを外すと2個目の collect が偽の recovered lock を書いて稼働中の利用セッションを分断する／スクリーンショットの mtime を撮影時刻へ合わせ忘れると、IDF が取り込み時刻を記録時刻にする |
| `src/chrome_ext/**`・`src/autolog/internal/ingest/**` | [autolog-chrome-ext](.claude/skills/autolog-chrome-ext/SKILL.md) | ロックの中から `withState` を呼ぶと**デッドロックして以後の収集が丸ごと止まる**（入口だけでロックを取る規約。例外は `removeFromQueue`）／区間を消してからイベントを積む順にすると、その間に Service Worker が止まったとき区間ごと失われる／4xx で件数を半分に絞る処理を外すと、不正な1件のせいでキューが永久に詰まる／受け口を 127.0.0.1 以外へ bind するとトークンだけが防御になる |
| `src/android/**`・`src/autolog/internal/config/timezone_android.go` | [autolog-android](.claude/skills/autolog-android/SKILL.md) | 書き出しファイル名が衝突すると `renameTo` が**黙って上書き**し、端末側から削除済みの生ログが失われる／`onUpgrade` でテーブルを作り直すと、GPX は毎回全点から作り直すため**当日分の軌跡が短くなって消える**／記録種別の既定値を false にすると、更新した時点でそれまで記録できていたものが黙って止まる／「端末の利用」に切り替えを置くと、区間を閉じる土台が消えてアプリ利用も再生も閉じられなくなる／マイク種別を位置情報と同じ形で `startForeground` に足すと、再起動のたびに失敗して `stopSelf` へ落ち収集が丸ごと止まる／`time.Local` は UTC 固定なので記録時刻が全部 +00:00 になり、文字列で並べ替える以上あとから直しにくい壊れ方をする |
| `src/scripts/**` | [autolog-powershell](.claude/skills/autolog-powershell/SKILL.md) | BOM なしで保存すると 5.1 が Shift_JIS と誤読し、日本語コメントの2バイト目が改行を飲んで**次の行が黙って消える**／`-File` 起動では param ブロックの `$PSScriptRoot` が空になり、タスクスケジューラからだけ落ちる／`2>&1` した native の stderr が終了エラーになり、成功しているのに失敗扱いになる／`$CutoffLagMinutes`（2分）は `cmd_import.go` の `untilNowCutoffLag` と**二重持ち**で、片方だけ変えると結合待ちの窓がずれる |
| `package.json`・`src/tools/**`・`src/android/**/build.gradle.kts`・`.gitattributes` | [autolog-build-release](.claude/skills/autolog-build-release/SKILL.md) | `versionCode` を上げ忘れると Android が引き下げを拒み、**アンインストール（＝未書き出しの記録の喪失）**なしには入れ替えられない／debug 署名で配ると署名鍵が機械ごとの debug keystore になり、別の機械で組み直した APK を上書きできない／クロスコンパイルは中身を検査しないと、Android 向けのつもりが Windows の MZ バイナリのまま出来上がっても気づけない／`*.ps1 text eol=crlf` を外すと 5.1 の実行環境が壊れる |
| `AGENTS.md`・`CLAUDE.md`・`.claude/skills/**`・`documents/**`・`README.md`・`src/README.md` | [autolog-docs](.claude/skills/autolog-docs/SKILL.md) | `npm test` に含まれる `verify_docs` が落ちる／ルーティング表に行が無いスキルは、パス連動のスキル機構を持たないエージェント（Codex CLI・Cursor・Gemini CLI）から**永遠に読まれない**／`.gitignore` を戻すと skills が新しいクローンに存在せず、検査が静かにゼロ件になる |

<!-- ROUTING-TABLE:END -->

### 症状から引く

| 症状 | 読む |
|---|---|
| 取り込んだはずのイベントが gkill に無い／同じ範囲が二度と入らない | autolog-pipeline |
| 同じ記録が2件になる | autolog-pipeline, autolog-android, autolog-chrome-ext |
| 取り込みが途中から全部書けなくなる／ログインできない | autolog-gkill-api |
| URLog を入れたら gkill が重い／外向き通信が増えた | autolog-gkill-api |
| SSID が空になる／Bluetooth 機器が1つも出ない | autolog-windows-collect |
| ログオフ・コンソールを閉じると最後の記録が消える | autolog-windows-collect |
| collect を2つ起動したら利用セッションが切れ切れになった | autolog-windows-collect |
| Chrome の閲覧・再生が送られてこない／キューが減らない | autolog-chrome-ext |
| 拡張が固まる／以後どのイベントも送られない | autolog-chrome-ext（`withState` のデッドロック） |
| Android が更新後に何も記録しなくなった | autolog-android（記録種別の既定値） |
| 当日の GPX が短くなる／書き出したはずの生ログが無い | autolog-android |
| 時刻が +00:00 で記録される／PC の記録と並び順が合わない | autolog-android（`time.Local`） |
| タスクスケジューラからだけ落ちる／日本語コメントの次の行が消えた | autolog-powershell |
| APK を入れ替えられない／署名が合わない | autolog-build-release |
| `npm run verify_docs` が落ちた | autolog-docs |

## Build & Development Commands

| コマンド | 用途 |
| --- | --- |
| `npm test` | `verify_docs` → Go テスト → Chrome 拡張テスト |
| `npm run verify_docs` | 資料の機械検査（入口サイズ・スキル索引・リンク・件数・個人情報 ほか）。`--list` で実測値 |
| `npm run build` | Windows 向けにビルド（`release/windows_amd64/autolog.exe` を出す） |
| `npm run build_android_arm64` | Android (arm64) 向けにビルド |
| `npm run build_android_apk` | 収集アプリの APK を作る |
| `npm run release` | 全プラットフォーム向けにビルドして成果物を検査 |
| `cd src\autolog && go test ./...` | Go のテスト |
| `npm run test_chrome_ext` | Chrome 拡張のテスト（Node 標準ランナー + chrome スタブ） |
| `cd src\android && .\gradlew.bat --% assembleDebug -PversionName=X -PversionCode=N` | APK |
| `.\src\scripts\check_connection.ps1` | gkill へ繋がるか確認（書き込まない） |
| `.\src\scripts\run_import.ps1 -DryRun -UntilNow` | 取り込みの内容確認 |

**前提:** Go 1.26 以上、JDK 17 + Android SDK（APK を作る場合）。
CGO は使わない（SQLite は純 Go）。

ビルドの罠・APK の署名・成果物の検査は [autolog-build-release](.claude/skills/autolog-build-release/SKILL.md)。

## Architecture

### ディレクトリ

```
src/
  autolog/     Go の CLI。go.mod はここ（module: github.com/mt3hr/gkill_autolog/src/autolog）
  android/     Android 収集アプリ (com.mt3hr.gkill_autolog)
  chrome_ext/  Chrome 拡張 (Manifest V3)
  scripts/     PowerShell スクリプト
  tools/       ビルドの小物（Node。npm スクリプトから呼ばれる）
documents/
  reverse/     設計資料
```

gkill 本体と同じく、実装は `src/` の下、資料は `documents/reverse/` に置く。

### データの流れ

```
収集 → raw.db（追記専用）→ normalize（提案）→ gkill HTTP API
                                                    ↓
                                               ledger.db（書き込み済み）
```

スクリーンショットと GPX（位置情報）は別経路。raw.db と import を通らず、
撮って（書いて）置くだけ。gkill へ入れるのは同期スクリプトと
`gkill_server idf` / gpslog rep の役目。

### サブコマンド

`collect`（常駐収集）/ `import`（取り込み）/ `screenshot`（1枚撮る）/ `status`（状況表示）

### パッケージ

`rawlog`（生ログ）/ `collect`（Windows 収集）/ `winapi` / `shot` /
`ingest`（Chrome の受け口）/ `inbox`（Android の受け口）/
`normalize`（**中核**）/ `gkillclient` / `ledger` / `config` /
`proclock`（多重起動防止のファイルロック）

## 設計上の約束

詳細は `documents/reverse/design-philosophy.md`。

- **観測できた事実だけを記録する。** 目的・感情・集中状態を推測しない。要約しない
- **判断は決定的なルールで行う。** LLM に取捨を任せない。記録した事実のうち何を
  gkill へ出さないかの除外は `url_denylist.txt` と `notification_denylist.txt` のみ
  （要件で決まっている固定の取捨、たとえばブラウザ内部ページや YouTube 系の閲覧の
  除外は normalize のコードにある）。何を観測するか自体の取捨は別で、
  Android は設定画面のチェックボックスで種類ごとに選べる
- **生ログは消さない。** 追記専用。`(端末, event_id)` で一意なので再取り込みが安全
- **失敗は記録せず次回やり直す。** 台帳には成功した分だけ載せる
- **端末名・利用者名をコードに書かない。** 特定環境の決め打ちを置かない。設定で決める
  （既定値は「ホスト名・機種名から導く」のような環境非依存の導出だけ）
- **エラーメッセージは利用者に見える経路（config・cmd・gkillclient・inbox）は日本語。**
  内部診断（winapi・rawlog の低層など）は英語のままでよい

## テスト

`internal/normalize` が最重要。閾値・結合・持ち越しの境界値を検証している。

**本番の gkill に対して試さない。** 別のホームと別のポートで gkill を起動して検証する
（`documents/reverse/testing-guide.md`）。

## Language

コード内のコメント、コミットメッセージ、ドキュメントは日本語。

Go の書き方は gkill 本体に合わせる。`slices.SortFunc`（`sort.Slice` は使わない）、
`for range n`、`any`（`interface{}` は使わない）、複数エラーは `errors.Join`。

## Documentation

- 領域別の禁止文・不変条件の正本: `.claude/skills/autolog-*/SKILL.md`（上のルーティング表から引く）
- 現在どうなっているか（設計資料）: `documents/reverse/`（索引: [documents/reverse/README.md](documents/reverse/README.md)）
- 実装の入口: [src/README.md](src/README.md)
- 資料の件数・リンク・ファイル名実在・スキル索引は `npm run verify_docs` が機械検査する。
  資料層の保守手順は [autolog-docs](.claude/skills/autolog-docs/SKILL.md) スキルにある

## AI エージェントへの約束

- **個人情報・実環境の情報をリポジトリへ入れない（最重要）。** 実在の利用者ID・人名・メールアドレス・
  端末のローカル絶対パス・実データの中身を、コード・資料・テストデータ・
  コミットメッセージのどこにも書かない。例示パスは `$AUTOLOG_HOME` や `$HOME`、
  `〈ユーザー名〉` のプレースホルダで書く。`npm run verify_docs` が資料への混入をパターン検査するが、
  検査は網でしかない — 書く前に止めることがすべて。
- **このファイルと `CLAUDE.md` に領域別の規約本文を書き足さない。** 正本は `.claude/skills/*/SKILL.md`。
  ここが太ると全タスクの常時コンテキストを食う。サイズ上限（verify_docs が検査）に当たったら、
  上限を上げるのではなく中身をスキルへ落とすこと。
- 資料に書いた件数・リンク・ファイル名は `npm run verify_docs`（`npm test` に含まれる）が機械検査する。
  数字を書いたら `src/tools/verify_docs.mjs` の検査にも載せること。
- 作業報告・コミットメッセージ・新規コメントは日本語で書く。
