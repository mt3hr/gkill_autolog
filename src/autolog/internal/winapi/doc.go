// Package winapi は Windows 収集プログラムが使う Win32 API の薄いラッパ。
//
// CGO は使わず golang.org/x/sys/windows の LazyDLL 経由で呼ぶ。
// 中身は全て _windows.go に置いてあるため、他プラットフォームでは空のパッケージになる。
package winapi
