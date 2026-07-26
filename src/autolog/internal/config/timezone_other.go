//go:build !android

package config

// InitLocalTimezone は何もしない。
//
// Android 以外では Go が time.Local を正しく設定するため、手を入れる必要がない。
func InitLocalTimezone() error { return nil }
