package normalize

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// DenyList は URLog にしない URL のパターン。
//
// 要件 §7.1 の除外項目のうち、機械的に判別できないもの
// （広告ページ、閲覧前の一時的な中継ページ、その他不要と判断するページ）を
// 利用者が自分で列挙するために使う。
//
// 書式は1行1パターン。
//   - `#` で始まる行と空行は無視する
//   - 既定は**部分一致**（大文字小文字を区別しない）。例: doubleclick.net
//   - `re:` で始まる行は正規表現。例: re:^https://example\.com/ads/
//     こちらは大文字小文字を区別する。無視したいときは (?i) を先頭に付ける
type DenyList struct {
	substrings []string
	regexps    []*regexp.Regexp
}

// ParseDenyList は除外リストを読み込む。
func ParseDenyList(r io.Reader) (*DenyList, error) {
	list := &DenyList{}

	scanner := bufio.NewScanner(r)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if pattern, ok := strings.CutPrefix(line, "re:"); ok {
			compiled, err := regexp.Compile(strings.TrimSpace(pattern))
			if err != nil {
				return nil, fmt.Errorf("除外リスト %d 行目の正規表現が不正: %w", lineNumber, err)
			}
			list.regexps = append(list.regexps, compiled)
			continue
		}
		list.substrings = append(list.substrings, strings.ToLower(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("除外リストを読めなかった: %w", err)
	}
	return list, nil
}

// Matches は URL が除外対象かを返す。
func (d *DenyList) Matches(url string) bool {
	if d == nil {
		return false
	}

	lower := strings.ToLower(url)
	for _, substring := range d.substrings {
		if strings.Contains(lower, substring) {
			return true
		}
	}
	for _, re := range d.regexps {
		if re.MatchString(url) {
			return true
		}
	}
	return false
}

// Len は登録されているパターン数を返す。
func (d *DenyList) Len() int {
	if d == nil {
		return 0
	}
	return len(d.substrings) + len(d.regexps)
}

// DefaultDenyList は初期状態で置いておく除外リストの内容。
const DefaultDenyList = `# URLog にしない URL のパターン
#
# 1行1パターン。# で始まる行と空行は無視する。
# 既定は部分一致（大文字小文字を区別しない）。
# re: で始めると正規表現になる。
#
# ここに書いたものは gkill へ書き込まれない。
# 迷ったら書かないでおく。あとから rykv でタグ絞り込みして消せる。

# --- 広告・計測 ---
doubleclick.net
googlesyndication.com
googleadservices.com
google-analytics.com
/adserver/
# 注意: utm_ 付きの URL は計測パラメータが付いただけの普通の記事のこともある。
# 巻き込みたくなければこの行を # で消す
utm_source=

# --- 認証・中継ページ ---
accounts.google.com/signin
login.microsoftonline.com
/oauth2/authorize
/saml2/

# --- 検索エンジンの中継 ---
google.com/url?
l.facebook.com/l.php
# t.co は短い名前なので部分一致にしない。tenant.co のような別ドメインまで当たる
re:^https?://t\.co/

# --- 一覧・タイムラインのトップ（何を見たかの記録として薄い）---
x.com/home
re:^https://(www\.)?reddit\.com/?$

# YouTube と YouTube Music はここに書く必要がない。
# 個別コンテンツだけを残すため、閲覧区間からは URLog を作らない作りになっている。

# --- 自分で足す ---
`
