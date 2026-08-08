// Package tokenizer は本システム唯一の正規化・トークン化実装である（仕様書5章）。
//
// インデックス構築側（kakoctl）と検索API側（kakoapi）は必ずこのパッケージを
// import すること。別実装を作った瞬間、索引と検索が静かに食い違いシステムは壊れる。
package tokenizer

import (
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

// Version は正規化ロジックのバージョン（仕様書16.3）。
// ロジックを変更したら必ずインクリメントし、インデックス全体を再構築すること。
// インデックス構築時にこの値を記録し、API 起動時に照合して不一致なら起動を拒否する。
const Version = 1

// TitleMax は保持する最大文字数（書記素クラスタ数）。正規化後に適用する（仕様書5.4）。
const TitleMax = 512

// Normalize は仕様書5.1のパイプラインをこの順序で適用する:
//  1. 制御文字（Cc）・U+FFFD の除去
//  2. NFKC 正規化
//  3. NFC 適用
//  4. ASCII 範囲のみ小文字化
//  5. 空白類の全除去
//
// ひらがな⇄カタカナの統合は行わない（仕様書5.1、既定OFF）。
func Normalize(s string) string {
	var pre strings.Builder
	pre.Grow(len(s))
	for _, r := range s {
		// unicode.IsControl は Cc のみ真。ZWJ(U+200D) 等の Cf は絵文字連結に
		// 必要なため、ここで落としてはならない。
		if r == '�' || unicode.IsControl(r) {
			continue
		}
		pre.WriteRune(r)
	}
	t := norm.NFC.String(norm.NFKC.String(pre.String()))
	var out strings.Builder
	out.Grow(len(t))
	for _, r := range t {
		if unicode.IsSpace(r) {
			continue
		}
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		out.WriteRune(r)
	}
	return out.String()
}

// Tokens は正規化後の文字列を書記素クラスタ単位（uniseg）で分割して返す。
// サロゲートペア・異体字セレクタ・絵文字ZWJ連結は1トークンに保たれる。
// TitleMax 個で打ち切る。空入力は nil を返す（例外を投げない）。
func Tokens(s string) []string {
	n := Normalize(s)
	if n == "" {
		return nil
	}
	toks := make([]string, 0, len(n)/3)
	state := -1
	var cluster string
	for len(n) > 0 {
		cluster, n, _, state = uniseg.FirstGraphemeClusterInString(n, state)
		toks = append(toks, cluster)
		if len(toks) == TitleMax {
			break
		}
	}
	return toks
}

// IndexText はインデックス投入（TSV）用のトークン列を返す。
//
// ※ 両側で処理が非対称になる唯一の箇所（仕様書5.3）:
// TSV 側はエスケープせず生のまま格納し、検索クエリ側だけ EscapeToken を通す。
// ここに Manticore エスケープを入れてはならない。
func IndexText(s string) string {
	return strings.Join(Tokens(s), " ")
}

// reserved は Manticore のクエリ構文で意味を持つ文字（仕様書5.3）。
var reserved = [128]bool{
	'\\': true, '(': true, ')': true, '|': true, '-': true,
	'!': true, '@': true, '~': true, '"': true, '&': true,
	'/': true, '^': true, '$': true, '=': true, '<': true,
}

// EscapeToken は検索クエリを組み立てる側でのみ使う（仕様書5.3）。
func EscapeToken(tok string) string {
	var b strings.Builder
	b.Grow(len(tok) * 2)
	for _, r := range tok {
		if r < 128 && reserved[r] {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// QueryTokens は検索語をトークン化し、各トークンをエスケープして返す。
// フレーズ組み立て（引用符で囲む）は呼び出し側（internal/index）の責務。
func QueryTokens(s string) []string {
	toks := Tokens(s)
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = EscapeToken(t)
	}
	return out
}
