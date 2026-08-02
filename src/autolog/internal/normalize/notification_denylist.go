package normalize

// 通知の除外リスト。
//
// 書式と照合の仕組みは URL の除外リスト（denylist.go の DenyList）と同じものを使う。
// 照合の相手が URL ではなくパッケージ名・アプリ名・チャンネルIDになるだけで、
// 「除外は決定的なルールと利用者が書いた一覧だけで決まる」という
// 設計上の約束は変わらない（documents/reverse/design-philosophy.md）。
//
// パッケージ名とアプリ名の両方を照合する。
// 収集元によってはパッケージ名が取れないことがあり、
// そのときでもアプリ名で落とせるようにするため。
//
// チャンネルIDも照合する。ダウンロード完了のように、
// 同じアプリの通知でも一部だけ落としたいものがあるため。

// DefaultNotificationDenyList は初期状態で置いておく通知の除外リストの内容。
//
// 既定で有効にするのは自動化ツールの通知とダウンロードの通知だけにする。
// これらは「利用者が何かをした記録」ではなく、
// 利用者が仕込んだ処理が動いたことの副産物なので、観測できた事実ではあっても
// ライフログとして残す意味がない。
//
// システム由来の通知は端末によって出方が違うので、例として挙げるだけにする。
const DefaultNotificationDenyList = `# Kmemo にしない通知のパターン
#
# 1行1パターン。# で始まる行と空行は無視する。
# 既定は部分一致（大文字小文字を区別しない）。
# re: で始めると正規表現になる。こちらは大文字小文字を区別するので、
# 無視したいときは (?i) を先頭に付ける。
#
# 照合の相手はパッケージ名・アプリ名・通知チャンネルID。
# どれかに当たれば、その通知は gkill へ書き込まれない。
#   例) com.termux.api        パッケージ名で落とす
#       Tasker                アプリ名で落とす
#       re:^com\.google\..*   正規表現で落とす
#
# チャンネルIDはアプリが通知の種類ごとに付けている名前。
# 同じアプリの通知でも一部だけ落としたいときに使う。
# 実際の値は生ログの payload.channel_id で確かめられる。
# downloads のような短い語なので、部分一致で書かず
# re:(?i)^downloads$ のように前後を留めた正規表現で書くこと。
# そうしないとアプリ名やパッケージ名の一部にも当たる。
#
# 端末に登録されている値は adb でも一覧できる。
#   adb shell dumpsys notification --noredact
# AppSettings: <パッケージ名> の下に NotificationChannel{mId='...'} が並ぶ。

# --- 自動化ツールの通知 ---
# 同期スクリプトやタスクの実行結果。自分で仕込んだ処理が動いた副産物なので残さない。
com.termux.api
net.dinglisch.android.taskerm

# --- ダウンロードの通知 ---
# 何をダウンロードしたかは、そのあとの操作のほうに残る。
# 完了の知らせ自体は処理の結果でしかないので残さない。
# ダウンロード専用のアプリはパッケージ名で落とす。
com.android.providers.downloads
# Chrome のように他の通知も出すアプリは、チャンネルIDで
# ダウンロードの分だけを落とす。アプリ名がちょうど Downloads のものにも当たる。
#
# Chrome はダウンロードのチャンネルを2つ持っている。
# 進行中が downloads で、「ダウンロードが完了しました」は completed_downloads。
# 残る意味がないのは後者なので、completed_ が付く方を必ず含めること
# (Pixel 9a / Galaxy Tab S11 の dumpsys notification で確認)。
re:(?i)^(completed_)?downloads$
# 端末によっては「ファイル」からも出る。うるさければ # を外す。
#com.google.android.documentsui
# Samsung Internet のダウンロード。使っていれば # を外す。
#re:(?i)^SBROWSER_DOWNLOADS_NOTIFICATION_CHANNEL$

# --- システムの通知 ---
# 端末によって出方が違うので既定では有効にしない。うるさければ # を外す。
#android
#com.google.android.gms
#com.google.android.odad
#com.google.android.packageinstaller
`
