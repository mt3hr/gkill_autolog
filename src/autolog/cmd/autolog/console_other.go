//go:build !windows

package main

import "log/slog"

// hideConsoleIfLaunchedByScheduler は Windows 以外では何もしない。
// コンソールウィンドウという概念が無い。
func hideConsoleIfLaunchedByScheduler(_ *slog.Logger) {}
