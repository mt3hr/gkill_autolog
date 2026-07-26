# gkill_autolog 設計資料集

## 概要

このディレクトリには gkill_autolog の設計資料を収録しています。

gkill_autolog は、PC・Android 端末・Chrome から**客観的な操作ログを自動で集め**、
[gkill](https://github.com/mt3hr/gkill) の Kyou として記録するツール群です。

### 中心にある考え方

**記録するのは観測できた事実だけ**です。目的・感情・集中状態を推測しません。
何を残すかはすべて決定的なルールで決まり、実行のたびに同じ入力からは同じ結果が出ます。

詳しくは [design-philosophy.md](design-philosophy.md) を参照してください。

## 推奨する読み順

前の資料の知識が後の資料の理解を助けます。

1. **[glossary.md](glossary.md)** — 用語集。最初に読んでください。以降の全資料で使う語（生ログ、提案、台帳、端末別ユーザー等）を定義しています。
2. **[design-philosophy.md](design-philosophy.md)** — 設計思想。なぜこの形になったか、何を避けたかを記録しています。
3. **[folder-structure.md](folder-structure.md)** — フォルダ構成。どこに何があるかを把握します。
4. **[requirements.md](requirements.md)** — 要件定義。何をどう記録するかの取り決めです。閾値や除外条件の根拠はここにあります。
5. **[program-spec.md](program-spec.md)** — 実装仕様。パッケージ構成、データの流れ、主要な処理の詳細です。
6. **[sequence-diagrams.md](sequence-diagrams.md)** — シーケンス図。収集から取り込みまでの処理の流れです。
7. **[dev-setup.md](dev-setup.md)** — 開発環境の構築とビルド手順です。
8. **[operations-guide.md](operations-guide.md)** — 導入と運用の手順。端末を増やすときもここを見ます。
9. **[testing-guide.md](testing-guide.md)** — テストの実行方法と、本番を汚さない検証手順です。

## 資料の対応関係

| 資料 | 対象読者 | 内容 |
| --- | --- | --- |
| glossary | 全員 | 用語の定義 |
| design-philosophy | 開発者 | 設計判断とその理由 |
| folder-structure | 開発者 | ディレクトリ構成 |
| requirements | 全員 | 記録の取り決め・閾値 |
| program-spec | 開発者 | 実装の詳細 |
| sequence-diagrams | 開発者 | 処理の流れ |
| dev-setup | 開発者 | ビルド手順 |
| operations-guide | 利用者 | 導入・運用 |
| testing-guide | 開発者 | 検証手順 |
