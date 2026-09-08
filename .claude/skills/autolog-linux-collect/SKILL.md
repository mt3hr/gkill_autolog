---
name: autolog-linux-collect
description: "Linux の常駐収集（src/autolog/internal/linuxapi/・collect/platform_linux.go・shot/capture_linux.go）の約束。//go:build linux は Android にも入るので必ず linux && !android と書くこと、linuxapi の純粋ロジックにはタグを付けないこと（GOOS=linux のテストはクロスコンパイルでは走らない）、セッションの監視は D-Bus がゼロでも起動すること、logind の遅延インヒビタを取らないとサスペンド直前の書き出しが落ちること、ウィンドウを取れないなら入力も記録しないこと、Wayland で XWayland の _NET_ACTIVE_WINDOW を使うと偽の記録になること、evdev は /proc/bus/input/devices で選別し input_event の大きさは 24 バイトと 16 バイトがあること、LE_ を linuxapi で落としてはいけないこと、X11 の GetImage は帯に分けて読むことを扱う。src/autolog/internal/linuxapi/・internal/collect/platform_linux.go・internal/shot/capture_linux.go・src/scripts/linux/ を編集するとき必読。「Linux で何も記録されない」「Wayland でアプリ利用が出ない」の調査でも必読。"
---

# Linux 収集の不変条件

対象: `src/autolog/internal/linuxapi/**` / `internal/collect/platform_linux.go` /
`internal/shot/capture_linux.go` / `src/scripts/linux/**`

Windows 側の約束は [autolog-windows-collect](../autolog-windows-collect/SKILL.md) が正本。
収集のロジック本体（ウィンドウの状態機械・セッション・接続の差分検出）は
ビルドタグの無いファイルにあり、両者で共有している。

## `//go:build linux` は Android にも入る

`$GOROOT/src/go/build/build.go` に
`if ctxt.GOOS == "android" && name == "linux" { return true }` がある。

**Linux の収集は必ず `linux && !android`**、対応しない側は
`!windows && (!linux || android)` と書く。この3つで相互排他かつ網羅になる。

漏らすと Android 向けビルドが Linux の収集を取り込み、**Termux の `autolog` が
収集を始める**。Android の収集は Kotlin アプリの役目で、Termux 側は取り込みだけを行う。

`npm run vet_android` が唯一の自動検査。Linux 実装のファイルを足すたびに回すこと。

## 純粋ロジックにはビルドタグを付けない

`GOOS=linux go test` は**クロスコンパイルしたテストを実行できない**。
タグを付けた関数は Windows の開発機で一度も走らない。

解析・計算だけの部分はタグ無しのファイルへ置く。現在そうしてあるもの:

| ファイル | 中身 |
|---|---|
| `power.go` | sysfs から AC 接続の有無を読む |
| `inputkind.go` | evdev のイベント分類、`input_event` の大きさ |
| `procinput.go` | `/proc/bus/input/devices` の選別 |
| `desktopentry.go` | `.desktop` の解析とロケール解決 |
| `lockstate.go` | ロック状態の重複排除 |
| `ssid.go` | SSID のバイト列と コマンド出力の解釈 |
| `stripe.go` | X11 `GetImage` の帯分割 |
| `captureargs.go` | 撮影コマンドの引数の組み立てと画像のデコード |
| `swaytree.go` / `hyprland.go` | 合成器の IPC の解析 |
| `env.go` | セッション種別の判定 |
| `x11input.go` | 押された瞬間の判定 |

`doc.go` にタグを付けないことで、パッケージ全体が全 GOOS で
「純粋関数だけのパッケージ」としてコンパイルされる（`winapi/doc.go` と同じ形）。

## セッションの監視は D-Bus がゼロでも起動する

`NewSessionWatcher` は**失敗しない**。繋がらなかった経路は `Warnings()` で返し、
起動時に1度だけ警告するだけ。

起動しないと `collector_start` / `collector_stop` が出ない。この2つは
`normalize/state.go` が「観測の切れ目」として使う唯一のマーカーで、
失うと **Wi-Fi・Bluetooth・充電の開いた区間が永久に閉じず**、
電源が入っていなかった時間までつながる。

`logon` / `logoff` は Linux では出さない。代わりは `collector_start` / `collector_stop`。
logind の `Active` を logon/logoff へ写すと、仮想端末を切り替えるたびに偽のセッション断が生まれる。

## logind の遅延インヒビタを取る

`PrepareForSleep(true)` はサスペンドの直前に飛ぶ。そのまま返すと
suspend のイベントが書き出される前に機械が眠り、接続区間を閉じるマーカーを失う。

起動時に `Manager.Inhibit("sleep:shutdown", ..., "delay")` で fd を握り、
イベントを流してから `sessionInhibitFlushGrace`（2秒）待って手放す。
systemd の既定 `InhibitDelayMaxSec` は5秒なので、その内側に収める。
**Windows の `finalFlushBusyWait`（3秒）と同じ考え方。**

`PrepareForSleep(false)`（復帰）のあとで**取り直す**こと。取り直さないと次のサスペンドで落ちる。

## ウィンドウを取れないなら入力も記録しない

`newLinuxWindowInputs` は**入力とウィンドウの両方が揃ったときだけ**コレクタを起動する。

入力だけあってウィンドウを取れないと、アプリ名が空の `input` イベントが溜まる。
`normalize/window.go` の `segmentTitle` はその場合**ウィンドウタイトルへ落ちる**ので、
要件 §6.4 が禁じている「ウィンドウタイトルが TimeIs のタイトルになる」状態を量産する。

代償として、異常終了時の補完（`recoverUnfinishedSession` の最終入力時刻）の精度が
セッション開始時刻まで落ちる。これは受け入れている。

## Wayland で XWayland の `_NET_ACTIVE_WINDOW` を使わない

Wayland のセッションでも XWayland のために `$DISPLAY` が入っていることが多い。
そこで X11 の経路を使うと、ネイティブの Wayland クライアントが前面のとき
`_NET_ACTIVE_WINDOW` は**古い値を返し続ける**。
「ずっと同じアプリを使っていた」という偽の記録になる。

判定は `Environment.IsWayland()`（`XDG_SESSION_TYPE` を先に見る）で行う。
GNOME・KDE の Wayland には標準の手段が無いので、警告を出して
アプリ利用だけを記録しない。`AUTOLOG_LINUX_WINDOW_COMMAND` が逃げ道。

## X11 ではロック中に前面ウィンドウを返さない

Windows の `GetForegroundWindow` はロック中に 0 を返すが、X11 では
ロッカー（`i3lock` など）が普通のウィンドウとして前面に来る。
`Foreground()` の冒頭で `IsSessionLocked()` を見ないと、
**「i3lock を1時間使っていた」という TimeIs** ができる。

## evdev は `/proc/bus/input/devices` で選別する

`ioctl (EVIOCGBIT)` を使わないのは、`_IOC` のビット配置がアーキテクチャ依存で
`linux/arm` 向けの配布を実機なしに確かめられないため。

**電源ボタン (`Power Button`) と `Video Bus` も `EV_KEY` を持つ。**
弾かずに開くと、ふたを閉じただけ・電源ボタンに触れただけで「操作した」ことになり、
使っていない時間にアプリ利用の TimeIs が生える。キーの数で切り分ける
（`keyboardKeyThreshold`）。

ビットマスクの1語のビット数は**桁数からは求められない**（カーネルが `%lx` で書くので
先頭の 0 が落ちる）。32bit のユーザー空間が 64bit のカーネルの上で動くこともあるので、
自分のポインタ幅からも決められない。**64bit と 32bit の両方で読んで足し合わせる。**

`struct input_event` は **64bit で 24 バイト、32bit で 16 バイト**。
取り違えると読んだバイト列が1件ずつずれ、でたらめな種別のイベントが延々と出る。

読み取りは止めない。詰まったら**イベントを捨てる**。読むのを止めるとカーネル側の
バッファが溢れて `SYN_DROPPED` になり、以後の解釈が狂う。

## `LE_` を linuxapi で落とさない

BlueZ の `Alias` をそのまま返す。同じ機器がクラシックと Low Energy の両方で
つながったときにまとめるのは `normalize/state.go` の `bluetoothDeviceName` の役目。
両側で落とすと二重に処理される。

名前順に並べたあと **`slices.Compact`** を掛けること。同じ名前の機器を2台つないでいると、
名前での差分検出が「1台切れて1台つながった」を延々と出す。

## 「取れない」と「未接続」を混ぜない

`CurrentSSID` の契約は winapi と同じで、`ok=false && err==nil` が未接続、
`err != nil` が取得失敗。

隠し SSID などで**接続中なのに SSID が空**のときは `ErrSSIDUnknown` を返す。
未接続として返すと、つながったままなのに偽の切断イベントが normalize へ流れ、
接続区間が途中で割れる。

手段が1つも無い種別は、`platform_linux.go` が「常に取得失敗を返す静かな関数」へ
差し替える。共通側 `netpower.go` は触らない。これをしないと15秒ごとに警告が出続ける。

## X11 の `GetImage` は帯に分けて読む

応答は1回あたりの上限に収まる必要があり、4K や複数モニタの仮想画面は必ず超える。
`imageStripes` で横帯に分ける。32bpp 以外は**並びを推測せずエラーにする**
（推測して撮ると色の壊れた画像が毎時保存され続ける）。

撮影コマンドの中間ファイルは一時ディレクトリへ作る。
**スクリーンショットの置き場へ作ると gkill の IDF 走査に拾われる。**

## 画面付きのログインセッションでなければ収集しない

`Environment.IsUserSession()` が false なら `ErrUnsupportedPlatform` を返す。

問題は「何も取れないこと」ではなく、**何も観測していないのに『端末利用』の
TimeIs が生えること**。`cmd_collect.go` は Chrome の受け口と定期撮影を続ける。

## 関連スキル

- [autolog-windows-collect](../autolog-windows-collect/SKILL.md) — 共有している収集ロジックの約束
- [autolog-pipeline](../autolog-pipeline/SKILL.md) — 書き込み先の生ログストアと観測の切れ目
- [autolog-build-release](../autolog-build-release/SKILL.md) — `vet_linux` / `vet_android` とビルド
