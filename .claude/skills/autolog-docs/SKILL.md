---
name: autolog-docs
description: "gkill_autolog の AI 向け資料層（AGENTS.md / CLAUDE.md / .claude/skills/ / documents/reverse/）の役割分担と保守手順。どれが何の正本か、スキルを足す・消す・改名したら AGENTS.md のルーティング表を同じコミットで更新すること、1スキルは SKILL.md 1ファイル、description は二重引用符でくくった1物理行にすること、入口のサイズ上限に当たったら上限を上げず中身をスキルへ落とすこと、npm run verify_docs が機械検査する内容（入口サイズ・双方向網羅・リンク・参照ファイル名の実在・件数・個人情報・アンカーコメント・gitignore）を扱う。AGENTS.md・CLAUDE.md・.claude/skills/・documents/・src/tools/verify_docs.mjs を編集するとき、verify_docs が落ちたとき必読。"
---

# 資料層の役割分担と保守手順

対象: `AGENTS.md` / `CLAUDE.md` / `.claude/skills/**` / `documents/**` /
`README.md` / `src/README.md` / `src/tools/verify_docs.mjs`

## 正本の分割

| 層 | 何を持つか |
|---|---|
| コードコメント | その場の規則と理由 |
| `AGENTS.md` | 全タスクで要る核 ＋ ルーティング表 ＋ 症状表。**領域別の規約本文は置かない** |
| `.claude/skills/autolog-*/SKILL.md` | 領域別の禁止文・不変条件の**正本** |
| `documents/reverse/` | **現在どうなっているか**（What）。要件・実装仕様・運用手順 |
| `CLAUDE.md`・他AI入口3本 | 導線だけ。規約本文ゼロ |

**スキルは「禁止文の正本」、`documents/reverse/` は「現状（What）」で層が違う。**
スキルへ移した文を reverse から消さない。ただし**両方に書く数字は増やさない**。

## スキルの保守手順

- スキルを足す/消す/改名したら、`AGENTS.md` のルーティング表を**同じコミットで**更新する
  （`checkSkills` が双方向網羅を検査して落とす）
- frontmatter は `name` と `description` の2つだけ。`name` は**ディレクトリ名と完全一致**
- `description` は**二重引用符でくくった1物理行**・80〜1024字・パスやファイル名を含むこと。
  内側に `"` を書かない（`^description:\s*"(.+)"\s*$` が貪欲マッチなので壊れる）。
  YAML の折り返し（`>-` や2行目インデント）も不可
- **1スキル = SKILL.md 1ファイル。** 同ディレクトリに補助 `.md` を置かない（`checkSkills` が落とす）
- **スキルに Mermaid を書かない**（`checkMermaid` の対象は `documents/reverse/` だけ。
  検査されない図はドリフトする）
- スキル間リンクは `../<name>/SKILL.md`。リポジトリ資料へは `../../../documents/...`（3階層固定）
- **`AGENTS.md` / `CLAUDE.md` に領域別の規約本文を書き足さない。** サイズ上限
  （`checkAgentEntrypoints`）に当たったら、**上限を上げるのではなく中身をスキルへ落とす**
- 高リスクなソースの先頭には
  `// 編集前に読む: .claude/skills/<name>/SKILL.md（この領域の不変条件の正本）` を置く。
  参照先の実在は `checkSkillAnchors` が検査するので、スキルを改名したらコメントも追随させる。
  **`.ps1` へのアンカーは任意** —— 編集で BOM が落ちると 5.1 が Shift_JIS 誤読して次の行が消える
- 節の太字リード文（`##` 見出し）は文言を変えない。将来 ADR を書いたとき出典アンカーになる

## `npm run verify_docs` が検査すること

`src/tools/verify_docs.mjs`。`npm test` の**先頭**で回る。

| 検査 | 内容 |
|---|---|
| `checkCounts` | ツリーから実測した件数と資料の記述の照合。`--list` で実測値を出す |
| `checkLinks` | 資料内 Markdown リンクの解決（error） |
| `checkPaths` | バッククォートの `src/...` 表記の実在（warning）。ルート・`src/autolog/`・`src/` の3基準で解決する |
| `checkDocFilenames` | 資料に載っているファイル名の実在。`Node.js` と gkill 本体/Go 標準ライブラリの名前は免除リストで除外 |
| `checkMermaid` | `documents/reverse/` の Mermaid ブロックの図種別 |
| `checkSkills` | スキル0本 tripwire・frontmatter・ルーティング表との双方向網羅・補助 `.md` 禁止 |
| `checkAgentEntrypoints` | `AGENTS.md` のバイト上限とマーカー・`CLAUDE.md` の `@AGENTS.md` 行と行数上限・他AI入口3本 |
| `checkPersonalInfo` | Windows 実パス／ホーム実パス／メールアドレス ＋ ローカル NG 語 |
| `checkSkillAnchors` | ソース内アンカーコメントが指すスキルの実在 |
| `checkGitignoreSkills` | `.gitignore` が `/.claude/*` + `!/.claude/skills/` の2行組であること |
| `checkNpmScripts` | 資料に出る `npm run <name>` が `package.json` に実在すること |

**数字を書いたら検査に載せる。** 資料に件数を書いたら `buildCountAssertions()` の `add()` を
**同じコミットで**足す（照合は素の部分文字列一致なので、語句が移った瞬間に赤くなる）。

**ADR とマニュアルの検査は移植していない。** このリポジトリに documents/adr/ も
マニュアルも無く、実体の無い検査は空回りするため。作るときに gkill 本体の
`checkADR` / `checkADRSources` を移植する。

**`verify_docs.mjs` 自身に単体テストは無い。** 検査を変えたら、わざと壊して落ちることを手で確認する
（表から1行消す／`description` を80字未満にする／スキルを1本 `mv` する／`.gitignore` を戻す／
アンカーの参照先を存在しない名前にする）。

## `.gitignore` の2行組を戻さない

```
/.claude/*
!/.claude/skills/
```

`.claude/` や `/.claude`（ディレクトリ除外形）に戻すと、git はネガティブパターンで
親ディレクトリの除外を打ち消せないため skills が追跡されなくなる。
**ローカルではファイルが残るので気づけず、新しいクローンでだけ落ちる。**
`checkGitignoreSkills` がこれを先に止める。

## 個人情報を資料へ書かない

実在の利用者ID・人名・メールアドレス・端末のローカル絶対パス・実データの中身を、
コード・資料・テストデータ・**コミットメッセージ**のどこにも書かない。
例示パスは `$AUTOLOG_HOME`・`$HOME`・`〈ユーザー名〉`・`〈接頭辞〉〈端末名〉` のプレースホルダで書く。
`checkPersonalInfo` がパターン検査するが、**検査は網でしかない — 書く前に止めることがすべて**。
パターンで表せない固有の NG 語は `verify_docs_personal_ngwords.local.txt`
（gitignore 済み・1行1語・**コミットしない**）に置くとローカルで検査に加わる。

## 関連スキル

- [autolog-build-release](../autolog-build-release/SKILL.md) — `npm test`（verify_docs を含む）の実行
