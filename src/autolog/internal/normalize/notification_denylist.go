package normalize

// 通知の除外リスト。
//
// 書式と照合の仕組みは URL の除外リスト（denylist.go の DenyList）と同じものを使う。
// 照合の相手が URL ではなくパッケージ名とアプリ名になるだけで、
// 「除外は決定的なルールと利用者が書いた一覧だけで決まる」という
// 設計上の約束は変わらない（documents/reverse/design-philosophy.md）。
//
// パッケージ名とアプリ名の両方を照合する。
// 収集元によってはパッケージ名が取れないことがあり、
// そのときでもアプリ名で落とせるようにするため。

// DefaultNotificationDenyList は初期状態で置いておく通知の除外リストの内容。
//
// 既定で有効にするのは自動化ツールの通知だけにする。
// これらは「利用者が何かをした記録」ではなく、
// 利用者が仕込んだ処理が動いたことの副産物なので、観測できた事実ではあっても
// ライフログとして残す意味がない。
//
// システム由来の通知は端末によって出方が違うので、例として挙げるだけにする。
const DefaultNotificationDenyList = `# Kmemo にしない通知のパターン
#
# 1行1パターン。# で始まる行と空行は無視する。
# 既定は部分一致（大文字小文字を区別しない）。
# re: で始めると正規表現になる。
#
# 照合の相手はパッケージ名とアプリ名の両方。
# どちらかに当たれば、その通知は gkill へ書き込まれない。
#   例) com.termux.api        パッケージ名で落とす
#       Tasker                アプリ名で落とす
#       re:^com\.google\..*   正規表現で落とす

# --- 自動化ツールの通知 ---
# 同期スクリプトやタスクの実行結果。自分で仕込んだ処理が動いた副産物なので残さない。
com.termux.api
net.dinglisch.android.taskerm

# --- システムの通知 ---
# 端末によって出方が違うので既定では有効にしない。うるさければ # を外す。
#android
#com.google.android.gms
#com.google.android.odad
#com.google.android.packageinstaller
`
