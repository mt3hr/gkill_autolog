# gkill 自動操作ログ収集「gkill_autolog」

## これはなに？ What's this?

PC・Android 端末・Chrome から**客観的な操作ログを自動で集めて**、
ライフログアプリケーション [gkill](https://github.com/mt3hr/gkill) に記録するツール群です。

手で打刻しなくても、あとから「この時間なにをしていたか」を振り返れるようになります。

### 記録されるもの

| 観測したこと | gkill での記録 |
| --- | --- |
| 端末を使っていた時間（ロック解除〜ロック） | TimeIs |
| どのウィンドウ・どのアプリを触っていたか | TimeIs |
| Wi-Fi・Bluetooth・充電の接続状況 | TimeIs |
| ブラウザで見ていたページ | URLog |
| 再生した動画・音楽 | TimeIs |
| 再生した動画・音楽のうちURLが分かるもの（Chromeでの再生など） | URLog（TimeIsと両方） |
| 受け取った通知（Android） | Kmemo |
| 定期的なスクリーンショット（間隔は設定できる） | IDF |
| 位置情報（Android） | GPX |

### 記録されないもの

**観測できた事実だけを残します。**
目的・感情・集中状態は推測しません。要約もしません。
取れなかった情報を補完することもしません。

何を残すかは決定的なルールと除外リストだけで決まります。
同じ入力からは、何度実行しても同じ結果になります。

## しくみ

各端末が自分で集め、**自分の gkill へ取り込みます。** 端末をまたぐ転送はしません。
そこから先の集約は、gkill 側の既存の同期に任せます。

```text
PC           収集 ──────┐
Chrome 拡張 ──HTTP─────┼→ 生ログ ─→ 整理 ─→ その PC の gkill ─┐
                                                              ├→ 既存の同期
Android      収集アプリ ─→ 生ログ ─→ 整理 ─→ その端末の gkill ─┘
```

生ログは追記専用で、**消しません。**
記録の規則を変えたときに、過去へ遡って作り直せるようにするためです。

書き込みに失敗した分は記録に残さないので、次の実行でやり直されます。

## 導入

手順は **[導入と運用](documents/reverse/operations-guide.md)** にあります。大まかには次のとおりです。

1. gkill に端末別のユーザーを作る
2. 設定ファイルを置く
3. ビルドして常駐させる
4. 同期スクリプトから取り込みを呼ぶ

### 必要なもの

| 対象 | 必要なもの |
| --- | --- |
| PC | Windows、Go 1.26 以上と Node.js 20 以上（ビルド時） |
| Android | Termux で動かしている gkill、JDK 17 と Android SDK（ビルド時） |
| Chrome | なし |

ビルドは npm スクリプトから行うので、ビルドする機械には Node.js (npm) が要ります。
CGO は使いません。C コンパイラは要りません。

## 資料の在り処

[設計資料集](documents/reverse/README.md) — 用語集から順に読めるようになっています。

| 資料 | 対象読者 | 内容 |
| --- | --- | --- |
| [用語集](documents/reverse/glossary.md) | 全員 | 生ログ・提案・台帳などの定義 |
| [設計思想](documents/reverse/design-philosophy.md) | 開発者 | なぜこの形になったか |
| [要件定義](documents/reverse/requirements.md) | 全員 | 記録の取り決めと閾値 |
| [実装仕様](documents/reverse/program-spec.md) | 開発者 | パッケージ構成と処理の詳細 |
| [導入と運用](documents/reverse/operations-guide.md) | 利用者 | 手順書 |
| [実装の入口](src/README.md) | 開発者 | ソースの歩き方 |

## ライセンス

MIT License. 詳しくは [LICENSE](LICENSE) を参照してください。
