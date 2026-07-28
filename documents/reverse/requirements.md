# gkill自動操作ログ収集システム 要件定義書

> **この文書は起点となった要件定義であり、実装と一致しない箇所があります。**
>
> 記録の対象・閾値・除外条件は、いまも実装の根拠として有効です。
> 一方、実現手段については以下が変わりました。理由は
> [design-philosophy.md](design-philosophy.md) に記録しています。
>
> | 要件での想定 | 実際の実装 |
> | --- | --- |
> | Claude Code が選別する | 決定的なルールと除外リストだけで決まる。Claude は関与しない |
> | gkill Write MCP 経由で書き込む | gkill の HTTP API を直接叩く |
> | 端末情報を RepName で識別する | 端末別ユーザーで書き分ける（RepName は指定できない） |
> | Android は PC へ生ログを送る | 各端末が自分の gkill へ取り込む |

## 1. 目的

Windows、AndroidおよびChromeから客観的な操作ログを自動収集し、Claude Codeが必要な粒度へ選別・変換したうえで、gkill Write MCPを通じてKyouとして記録する。

Claudeは利用者の目的、感情、集中状態、活動内容などを推測しない。役割は、生ログの整理、重複や不要イベントの除外、gkill向けデータへの変換に限定する。

## 2. 全体構成

```text
Windows収集プログラム
Android収集アプリ
Chrome拡張
スクリーンショット収集
        ↓
端末別の生ログ保存
        ↓
X1 Yogaへ集約
        ↓
毎日午前4時にClaude Codeを起動
        ↓
ルール処理・Claudeによる選別
        ↓
gkill ApplicationConfig取得
        ↓
既存構成に合うRepNameを解決
        ↓
gkill Write MCPへ完全自動書き込み
```

処理できなかったログは削除せず、次回以降のバッチで再処理できる状態を維持する。

## 3. 基本方針

* 客観的な操作事実だけを記録する
* 日次要約、集中時間、活動目的、感情、成果の推測は行わない
* ブラウザは閲覧ページを具体的なURLogとして残す
* アプリ操作はTimeIsとして残す
* YouTubeとYouTube Musicは個別コンテンツのURLogだけを残す
* スクリーンショットはClaudeを経由させず、IDFとして取り込む
* Gitコミットは既存のGitCommitLogがあるため対象外
* 生ログは当面削除しない
* 初期段階からgkillへ完全自動書き込みする
* 承認画面、ルール学習、二重登録防止は初期スコープ外とする

## 4. gkill Repository

RepNameをコードへ固定しない。

夜間バッチ実行時にgkill MCPからApplicationConfigを取得し、既存のRepository構造、データ型、端末別Repositoryの命名習慣に従って書き込み先を決定する。

端末情報はRepNameで識別可能にする。

対象端末：

* PC（Windows）
* Android 端末

以降、PC 側の端末名を `Laptop`、Android 側を `Phone` と書く。
実際の端末名は設定 (`AUTOLOG_DEVICE`) で決める。

TimeIs、URLog、Kmemo、IDFなどのデータ型は、既存のgkill構成に従う。

> **実装上の注記（2026-07-25 追記）**
> gkill Write MCP には書き込み先Repositoryを指定する手段が存在しない。
> `src/server/gkill/usecase/timeis.go` ほか全 add ユースケースが `repositories.WriteXxxRep` 固定であり、
> リクエストの `rep_name` は無視される。MCP側も `rep_name: ""` をハードコードしている。
> したがって本要件のうち「端末情報をRepNameで識別可能にする」はMCP経由では実現できない。
> 本システムでは **Tag により端末と収集元を識別する**（`docs/design-decisions.md` 参照）。
> スクリーンショットのIDF化のみ、ディレクトリ走査経由のため端末別RepNameを維持する。

## 5. 共通生ログ

各イベントには最低限、次の情報を保持する。

```json
{
  "schema_version": 1,
  "event_id": "端末内で一意なID",
  "device": "Laptop",
  "event_type": "window_activity",
  "start_time": "2026-07-25T10:00:00+09:00",
  "end_time": "2026-07-25T10:30:00+09:00",
  "captured_at": "2026-07-25T10:30:01+09:00",
  "payload": {}
}
```

端末内ではSQLiteなど、追記・障害復旧に強い形式を利用してよい。Claudeへ渡す際は、対象期間をJSONLなどへ出力する。

元のタイトル、URL、通知内容などは加工前の値を生ログに保持する。

## 6. Windows操作ログ

### 6.1 PC利用TimeIs

ロック解除またはログインから、ロック、ログアウト、シャットダウンまでをTimeIsとして記録する。

```text
タイトル：Windows利用
開始：ログインまたはロック解除
終了：ロック、ログアウト、シャットダウン
```

画面点灯だけでは開始しない。

再起動前後のセッションは結合しない。

収集プログラムが異常終了した場合は、最後に確認できた入力時刻またはシステムイベントを利用して未終了セッションを閉じる。

### 6.2 アクティブウィンドウ

操作として扱う入力：

* マウスクリック
* マウスホイール
* キー入力

マウス移動だけでは操作扱いにしない。

操作イベント発生時に、次を取得する。

* プロセスまたはアプリ名
* 前面ウィンドウの完全なタイトル
* イベント時刻

### 6.3 セッション化

* 5分間入力がなければアイドル終了
* 終了時刻はアイドル判定時刻ではなく、最後の入力時刻
* ウィンドウ切り替え後、1分以内に元のウィンドウへ戻った場合は前後を結合
* 1分以内だけ操作された割り込み側ウィンドウはTimeIs化しない
* 日付をまたいでも、連続セッションは1件のTimeIsとする
* 午前4時時点で継続中のセッションは処理せず、終了後の次回バッチで処理する

### 6.4 TimeIsタイトル

アプリ名とウィンドウタイトルを、生の値に近い状態で使用する。

自由な要約やタイトル整形は行わない。

例：

```text
gkill MCP Server - Google Chrome
main.go - gkill - Visual Studio Code
```

VS Codeについて、ファイルやRepositoryを別途解析する専用処理は設けない。ウィンドウタイトルから確認できる情報だけを使用する。

## 7. Windows Chrome

対象ブラウザはGoogle Chromeのみ。

正確な表示ページ、タブ切り替え、表示開始・終了、動画再生を取得するため、Chrome拡張を利用する。

取得項目：

* URL
* ページタイトル
* タブID
* ウィンドウID
* 表示開始時刻
* 表示終了時刻
* Chromeウィンドウが前面だったか
* 再生状態

### 7.1 URLog作成条件

* タブが前面になった時点で閲覧セッション開始
* 別タブまたは別アプリへ移動した時点で終了
* 1回の連続閲覧が30秒以上なら候補
* 同一URLでも再度表示した場合は別セッション
* 離れていた複数セッションの時間は合算しない
* RelatedTimeは閲覧開始時刻
* ページタイトルとURLをそのまま使用

保存対象：

* Google検索結果
* localhost
* gkill自身のページ
* 一般のWebページ

除外対象：

* 30秒未満の連続閲覧
* 新しいタブ
* Chrome設定画面
* 拡張機能画面
* リダイレクト途中
* 広告ページ
* 内容を閲覧する前の一時的な中継ページ
* Claudeが操作上の不要ページと判断したもの

Claudeはページの意味を要約せず、URLogとして残す必要があるかだけを判定する。

### 7.2 ChromeのTimeIs

Chrome操作は、他のWindowsアプリと同じ条件でTimeIs化する。

* 1分以内のページ切り替えは短時間割り込みとして除外可能
* 30秒以上1分未満のページは、TimeIsにならずURLogだけ残る場合がある
* TimeIsとURLogは独立して作成する

## 8. YouTube・YouTube Music

### 8.1 共通条件

作成するKyouは、個別コンテンツのURLを確定できた場合はURLogのみ。
確定できなかった場合はTimeIsのみ（8.2参照）。両方は作らない。

記録対象：

* YouTube動画
* YouTube Musicの個別楽曲

記録対象外：

* 広告
* プレイリストURL
* アルバムURL
* 30秒未満しか再生されなかったコンテンツ

判定条件：

* 実再生時間が合計30秒以上
* 一時停止時間は実再生時間に含めない
* 広告再生時間は含めない
* 同じ動画や楽曲でも、再生の都度URLogを作る
* RelatedTimeは再生開始時刻
* 個別コンテンツの正式URLを保存する
* タイトルは取得できた正式タイトルを使用する

### 8.2 Androidアプリ

対象：

* YouTubeアプリ
* YouTube Musicアプリ

取得優先順位：

1. Android MediaSessionからタイトル、アーティスト、再生状態を取得
2. MediaSessionのサムネイルURI `https://i.ytimg.com/vi/<動画ID>/...` から動画IDを確認する
3. Accessibility Serviceから画面表示情報を取得
4. root権限でアプリ内部の履歴などを確認

動画IDを確認できた場合は、そこから正規URLを組み立ててURLogにする。
サムネイルURIのパスに入っているのは動画IDそのものなので、これは推測ではない。

`METADATA_KEY_MEDIA_ID` は使わない。書式が動画IDと同じ11文字であっても
動画IDとは限らず、プレイリスト内の項目IDなど別のものが入りうる。
取り違えると存在しないURLを作り、gkillがそれを取得しに行って
エラーページのタイトルを保存してしまう。
誤ったURLを残すくらいなら、URL無しのTimeIsにする。

動画IDを確認できなかった場合は、**TimeIsとして記録する**。

* Titleは「タイトル - アーティスト」。アーティストを取得できなければタイトルのみ
* 区間は収集側が記録した壁時計の開始・終了。終了時刻が無い収集元では開始時刻に実再生秒数を足す
* タイトルを取得できなかった再生は記録しない。区間だけではアプリ利用のTimeIsと変わらないため
* 30秒未満を落とす条件はURLogと共通

検索URLや推測したURLは生成しない。Kmemoへの代替記録も行わない。
TimeIsに載せるのはMediaSessionから観測できたタイトル・アーティスト・区間だけで、
URLの代わりを埋め合わせることはしない。

YouTube MusicのTimeIsは、同じ時間帯のアプリ利用TimeIs（11.2）と重なる。
アプリ利用は「YouTube Musicを開いていた区間」、こちらは「何を再生したか」で、
記録している事実が違うため両方残す。

WindowsとAndroidの再生履歴は統合しない。

## 9. Windowsネットワーク・Bluetooth・充電

### 9.1 Wi-Fi

SSID単位の接続期間をTimeIs化する。

* BSSIDはgkillへ保存しない
* 有線LANは対象外
* 同じSSIDへ30秒以内に再接続した場合は前後を結合
* それ以上離れた場合は別TimeIs

### 9.2 Bluetooth

Bluetooth機器名単位の接続期間をTimeIs化する。

* 同じ機器へ1分以内に再接続した場合は結合
* 複数機器へ同時接続している場合は、それぞれ別TimeIs

### 9.3 充電

充電開始から終了までをTimeIs化する。

* 30秒以内に充電が再開された場合は結合
* 開始・終了時のバッテリー残量は記録しない
* 急速、USB、ワイヤレスなどの方式は区別しない

## 10. Windowsスクリーンショット

* 毎時00分に撮影
* Windowsロック中は撮影しない
* スリープ中は撮影しない
* 撮影できなかった時間の画像を後から補完しない
* 全モニターを結合した1枚の画像
* WebP形式
* Claudeには渡さない
* 所定フォルダへ保存
* gkillの既存バッチまたは取込処理でIDF化
* RelatedTimeは実際の撮影時刻

ファイル名例：

```text
<端末名>_2026-07-25_21-00-00.webp
```

## 11. Android端末利用

### 11.1 端末利用TimeIs

ロック解除から画面オフまたは再ロックまでを記録する。

```text
タイトル：端末利用
```

タイトルは設定 (`AUTOLOG_USAGE_TITLE`) で端末ごとに変えられる。

画面を点灯しただけでロック解除しなかった場合は記録しない。

### 11.2 アプリ利用TimeIs

必要な権限：

* 使用状況へのアクセス
* Accessibility Service
* 通知へのアクセス
* バッテリー最適化対象外
* Wi-Fi情報
* Bluetooth・付近のデバイス
* 必要な処理に限定したroot権限

条件：

* アプリが30秒以上利用された場合だけ記録
* 画面オフ中は記録しない
* ホーム・ランチャーは除外
* 設定アプリは除外
* 通知パネルは除外
* 分割画面では両アプリを同時にTimeIs化
* gkillのタイトルには表示上のアプリ名を使用
* パッケージ名はgkillへ保存しない
* 生ログには内部識別用としてパッケージ名を保持してよい
* 動画・音楽再生URLogとは別に記録する

例：

```text
20:00～20:30 Chrome
20:00～20:30 YouTube
```

## 12. Android Chrome

対象ブラウザはChromeのみ。

root権限でChromeの履歴DBを読み取り、アプリ利用ログと照合する。

取得候補：

* URL
* タイトル
* 訪問時刻
* Chrome利用セッション
* Accessibilityから得られた表示状態

Windowsと同じく、30秒以上の連続閲覧をURLog候補とする。

履歴DBに存在するだけで前面表示を確認できないページは、原則として自動登録しない。

Android ChromeのDB構造が変更された場合に備え、取得失敗時は生ログを保持し、書き込みを省略する。

## 13. Android通知

通知から取得する情報：

* アプリ名
* 通知タイトル
* 通知本文
* 通知時刻
* 通知IDなど内部の重複判定情報

gkillではKmemoとして記録する。

Kmemo本文：

```text
アプリ：Gmail
タイトル：○○さんからのメール
本文：明日の打ち合わせについて……
```

ルール：

* RelatedTimeは最初の通知時刻
* 同一通知IDの更新は最終状態だけを記録
* 常駐通知は除外
* タイトルと本文が両方空なら除外
* 同じ内容が短時間に再通知された場合は1件にまとめる
* タイトルだけの場合はタイトルのみ
* 本文だけの場合は本文のみ
* Gmail APIなどへは接続せず、Androidに表示された通知内容だけを扱う

## 14. Android Wi-Fi・Bluetooth・充電

Windows側と同じKyou変換規則を使用する。

* Wi-FiはSSID単位
* Wi-Fiの30秒以内の再接続は結合
* Bluetoothは機器名単位
* Bluetoothの1分以内の再接続は結合
* 充電の30秒以内の再開は結合
* バッテリー残量や充電方式は記録しない

## 15. Androidスクリーンショット

「できれば欲しい」項目として、初期必須スコープからは外す。

将来対応する場合は、Android側のOS制約とユーザ操作要求を確認したうえで追加する。

## 16. Claude Code夜間バッチ

実行環境：

* X1 Yoga
* Windowsタスクスケジューラ
* 毎日午前4時
* Claude Code非対話実行
* 専用SKILL.mdを利用
* gkill Write MCPへ接続

処理内容：

1. 前回処理位置から午前4時までの生ログを取得
2. 未終了セッションを除外
3. ウィンドウイベントをTimeIsセッションへ変換
4. Chrome履歴をURLog候補へ変換
5. 不要なWebページをClaudeが除外
6. YouTube・YouTube Musicの実再生時間を集計
7. 通知更新をまとめる
8. Wi-Fi、Bluetooth、充電の短時間切断を結合
9. gkill ApplicationConfigを取得
10. 既存のRepository構成に従ってRepNameを決定
11. gkill Write MCPで書き込む
12. 処理結果とエラーをローカルログへ保存

Claudeに禁止する処理：

* 活動目的の推測
* 「○○について調査していた」などの要約
* 感情や集中度の推測
* URLの捏造
* 動画IDの推測
* 通知本文の補完
* ウィンドウタイトルの意味的な言い換え
* 取得できなかった事実の補完

## 17. 障害時の扱い

* 1週間程度のログ滞留を許容
* 収集済み生ログは削除しない
* Claudeやgkill MCPが失敗した場合は、対象ログを次回再処理可能にする
* 継続中セッションは次回へ持ち越す
* 不完全なイベントはgkillへ書かず、生ログへ残す
* Androidからログが届いていない場合、Windows分だけ処理してよい
* スクリーンショット取込失敗は他のログ処理を停止させない

## 18. 初期スコープ外

* Git操作履歴
* ターミナルコマンド
* ファイル操作履歴
* クリップボード
* キーボードやマウスの操作量
* PC位置情報
* Android位置情報
* 歩数・移動状態
* 写真撮影
* 通話履歴
* メッセージ送受信履歴
* Gmail API接続
* Webサービスへの直接接続
* 日次要約
* 集中時間
* タスク抽出
* 既存KyouへのTag・Text追加
* 承認画面
* 人手承認フロー
* 自動ルール学習
* 二重登録防止
* Android定期スクリーンショット

## 19. 実装順序

1. 共通生ログスキーマ
2. Windows入力・ウィンドウ収集
3. Windows電源・ロックイベント
4. Chrome拡張
5. Windows Wi-Fi・Bluetooth・充電
6. Windowsスクリーンショット
7. Android常駐収集アプリ
8. Android Chrome履歴取得
9. Android通知収集
10. Android YouTube・YouTube Music取得
11. AndroidログのX1 Yogaへの転送
12. セッション正規化処理
13. Claude Code SKILL
14. ApplicationConfigによるRepName解決
15. gkill Write MCP連携
16. 午前4時の定期実行
17. 全必須ログを対象とした総合試験

## 20. 完了条件

次のデータが自動収集され、既存のgkill構成に従って正しいKyouとして書き込まれること。

### Windows

* Windows利用TimeIs
* 操作したアプリ・ウィンドウのTimeIs
* Chrome閲覧URLog
* YouTube・YouTube Music URLog
* Wi-Fi TimeIs
* Bluetooth TimeIs
* 充電TimeIs
* 毎時スクリーンショットIDF

### Android

* 端末利用TimeIs
* アプリ利用TimeIs
* Chrome閲覧URLog
* YouTube・YouTube Music URLog
* Wi-Fi TimeIs
* Bluetooth TimeIs
* 充電TimeIs
* 通知Kmemo

全項目について、Claudeが内容を推測せず、取得できた客観データだけを書き込むこと。
