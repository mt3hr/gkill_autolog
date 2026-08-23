---
name: autolog-build-release
description: "gkill_autolog のビルド・配布・テスト実行の約束。npm スクリプトの構成とクロスコンパイル、APK は release 署名固定で versionCode を package.json の version から導くこと（上げ忘れると Android が引き下げを拒んで入れ替えられない）、成果物は中身を見て別プラットフォームでないか検査すること、.gitattributes の改行固定（.ps1 は CRLF、gradlew と .sh は LF）、Go テストと Chrome 拡張テストの走らせ方、本番の gkill に対して試さないことを扱う。package.json・src/tools/build_go.mjs・build_apk.mjs・verify_release_artifacts.mjs・src/android/app/build.gradle.kts・.gitattributes を編集するとき、ビルドやリリースを頼まれたとき必読。「APK を入れ替えられない」「成果物が別プラットフォームのまま」の調査でも必読。"
---

# ビルド・配布・テストの不変条件

対象: `package.json` / `src/tools/**` / `src/android/**/build.gradle.kts` / `.gitattributes`

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## コマンド

| コマンド | 用途 |
| --- | --- |
| `npm run build` | Windows 向けにビルド（`release/windows_amd64/autolog.exe` を出す） |
| `npm run build_android_arm64` | Android (arm64) 向けにビルド |
| `npm run build_android_apk` | 収集アプリの APK を作る |
| `npm run release` | 全プラットフォーム向けにビルドして成果物を検査 |
| `npm run verify_docs` | 資料の機械検査（入口サイズ・スキル索引・リンク・個人情報 ほか） |
| `npm test` | `verify_docs` → Go テスト → Chrome 拡張テスト |
| `cd src\autolog && go test ./...` | Go のテスト |
| `npm run test_chrome_ext` | Chrome 拡張のテスト（Node 標準ランナー + chrome スタブ） |
| `cd src\android && .\gradlew.bat --% assembleDebug -PversionName=X -PversionCode=N` | APK |
| `.\src\scripts\check_connection.ps1` | gkill へ繋がるか確認（書き込まない） |
| `.\src\scripts\run_import.ps1 -DryRun -UntilNow` | 取り込みの内容確認 |

**前提:** Go 1.26 以上、JDK 17 + Android SDK（APK を作る場合）。
CGO は使わない（SQLite は純 Go）。

## APK は release 署名固定。versionCode を上げ忘れない

`src/tools/build_apk.mjs` の doc コメントが正本。

> バージョンは `package.json` の version から決める。
> versionCode は `major*10000 + minor*100 + patch`（1.0.0 なら 10000）。
> **Android は versionCode の引き下げを拒むので、上げ忘れると入れ替えられなくなる。**
>
> release ビルドで作る。debug ビルドは debuggable なうえ、署名鍵が
> 機械ごとの debug keystore になるため、別の機械で組み直すと署名不一致で
> 上書きできず、**アンインストール（＝未書き出しの記録の喪失）を強いられる。**
> 署名の設定は `~/.gradle/gradle.properties` か環境変数から読む
> （リポジトリには置かない。作り方は `documents/reverse/dev-setup.md`）。

## 成果物は中身を見て検査する

`src/tools/verify_release_artifacts.mjs` の doc コメントが正本。

> クロスコンパイルは設定を1つ間違えるだけで、中身が別プラットフォームの
> バイナリのまま出来上がる。実際、Windows 上で NDK の clang を指定したときに
> Android 向けのつもりが Windows のバイナリ (MZ) になったことがある。
> **ファイル名では気づけないので、中身を見て確かめる。**

## `.gitattributes` の改行固定を崩さない

`*.ps1 text eol=crlf`（PowerShell 5.1 の実行環境）/ `*.sh`・`gradlew` は `text eol=lf`
（そうでないと実行できない）。`*.apk` `*.jar` などは binary。

**作業ツリーは CRLF になっている**（`core.autocrlf` の過去のチェックアウトによる）。
`\n` を含む複数行のパターンで検索・置換すると**エラーも出さず0件**になるので、
置換は単一行パターンか `\r?\n` で行い、結果の件数を必ず数える。

## テスト

`internal/normalize` が最重要。閾値・結合・持ち越しの境界値を検証している。

**本番の gkill に対して試さない。** 別のホームと別のポートで gkill を起動して検証する
（`documents/reverse/testing-guide.md` の「検証用サーバの立て方」「後片付け」「やってはいけないこと」）。

## Go の書き方

gkill 本体に合わせる。`slices.SortFunc`（`sort.Slice` は使わない）、`for range n`、
`any`（`interface{}` は使わない）、複数エラーは `errors.Join`。

## 関連スキル

- [autolog-android](../autolog-android/SKILL.md) — APK の中身（収集アプリ）
- [autolog-powershell](../autolog-powershell/SKILL.md) — `.ps1` の BOM と CRLF
- [autolog-docs](../autolog-docs/SKILL.md) — `npm run verify_docs` が何を検査するか
