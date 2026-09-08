// Package linuxapi は Linux で操作ログを収集するための OS 依存処理をまとめる。
//
// winapi と対になる位置づけで、collect / shot はこのパッケージと winapi の
// どちらも直接は知らない。翻訳は collect/platform_*.go と shot/capture_*.go が行う。
//
// # ビルドタグ
//
// OS を叩くファイルは `//go:build linux && !android` にする。
// Go では GOOS=android が linux のビルドタグも満たすため（go/build の
// shouldBuild が android のとき linux を真として扱う）、linux だけで分けると
// Android 向けビルドが Linux の収集を取り込み、Termux の autolog が
// 収集を始めてしまう。Android の収集は Kotlin アプリの役目。
//
// 解析・計算だけの純粋な関数は**ビルドタグを付けない**。
// GOOS=linux のテストはクロスコンパイルでは実行できないため、
// タグを付けると Windows の開発機で一度も走らないコードになる。
// .desktop の解析、sysfs の判定、evdev のイベント分類、X11 GetImage の帯分割、
// ロック状態の重複排除は、どれもタグ無しのファイルへ置いてテストする。
// このファイルにタグを付けないことで、パッケージ全体が全 GOOS で
// 「純粋関数だけのパッケージ」としてコンパイルされる（winapi/doc.go と同じ形）。
//
// # 取れないものを黙って空にしない
//
// Linux はデスクトップ環境ごとに取れるものが違う。取得できなかったことを
// 「つながっていない」として扱うと、実際にはつながったままなのに
// 偽の切断イベントが生ログに残る。取得の失敗は必ずエラーで返し、
// 呼び出し側 (collect/platform_linux.go) が起動時に1度だけ警告する。
package linuxapi
