// sc.go は 2ch.sc の過去ログ倉庫からスレタイを取得する（5ch の倉庫が停止している板の補完用。
// docs/changes/2026-08-21.md）。
//
// 5ch 側（scrape.go）との構造の違い:
//
//	5ch: kako0000.html … 新しい順に1000件ずつ並ぶページを、アンカーに当たるまで進む
//	sc : kako/         … 「倉庫番号・格納数・subject.txt」の表が1枚。倉庫ごとに subject.txt
//
//	https://tomcat.2ch.sc/livejupiter/kako/
//	  <tr><td><a href="o1787/">#livejupiter/1787</a></td><td>214</td>...
//	https://tomcat.2ch.sc/livejupiter/kako/o1787/subject.txt   （Shift_JIS）
//	  1787155791.dat<>スレッドタイトル (2)
//
// ★ 倉庫番号は thread_key の上4桁、すなわち「スレの作成時刻」であって
// 「アーカイブされた時刻」ではない。長寿スレは古い倉庫番号に後から追加されるため、
// 「新しい倉庫だけ見る」では取りこぼす。索引ページの格納数を sc_kako_state に覚えておき、
// 数が変わった倉庫だけ subject.txt を取りに行くこと。
package scrape

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// KakoDir は sc の索引ページ1行（倉庫番号と格納数）。
type KakoDir struct {
	No    int // 倉庫番号。thread_key の上4桁（例: 1787）
	Count int // 格納数。-1 は索引ページから読み取れなかったことを表す（＝常に取得する）
}

// 例: <tr><td><a target="_blank" href="o1787/">#livejupiter/1787</a></td>
//
//	<td align="right">214</td><td align="right">18.49</td>
//	<td align="right"><a href="o1787/subject.txt">subject.txt</a></td></tr>
var scDirRe = regexp.MustCompile(`(?i)<a[^>]*href="o(\d+)/"[^>]*>[^<]*</a>\s*</td>\s*<td[^>]*>\s*(\d+)\s*</td>`)

// 格納数の列が読めなかったときの保険。subject.txt へのリンクだけを拾う。
var scSubjectLinkRe = regexp.MustCompile(`(?i)href="o(\d+)/subject\.txt"`)

// ParseKakoIndex は sc の過去ログ索引ページから倉庫の一覧を返す（ページ内の掲載順のまま）。
func ParseKakoIndex(page string) []KakoDir {
	ms := scDirRe.FindAllStringSubmatch(page, -1)
	out := make([]KakoDir, 0, len(ms))
	seen := map[int]bool{}
	for _, m := range ms {
		no, err := strconv.Atoi(m[1])
		if err != nil || seen[no] {
			continue
		}
		cnt, err := strconv.Atoi(m[2])
		if err != nil {
			cnt = -1
		}
		seen[no] = true
		out = append(out, KakoDir{No: no, Count: cnt})
	}
	if len(out) > 0 {
		return out
	}
	// 表の形が想定と違う板。格納数が分からないので毎回取得する扱いにする
	for _, m := range scSubjectLinkRe.FindAllStringSubmatch(page, -1) {
		no, err := strconv.Atoi(m[1])
		if err != nil || seen[no] {
			continue
		}
		seen[no] = true
		out = append(out, KakoDir{No: no, Count: -1})
	}
	return out
}

// 行末の "(123)" を切り出す。スレタイ自身が括弧数字で終わる場合と区別できないが、
// sc の subject.txt はレス数を必ずこの形で付けるため、末尾の1つだけを取る。
var scResRe = regexp.MustCompile(`\((\d+)\)\s*$`)

// ParseSubject は subject.txt（1行1スレ）を解析する。
//
//	1787155791.dat<>スレッドタイトル (2)
//
// 入力は UTF-8 へデコード済みであること（DecodeSC）。
func ParseSubject(text string) []Entry {
	lines := strings.Split(text, "\n")
	out := make([]Entry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		i := strings.Index(line, "<>")
		if i < 0 {
			continue
		}
		key := strings.TrimSuffix(line[:i], ".dat")
		tk, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			continue
		}
		rest := line[i+2:]
		var res int64
		if m := scResRe.FindStringSubmatchIndex(rest); m != nil {
			res, _ = strconv.ParseInt(rest[m[2]:m[3]], 10, 64)
			rest = rest[:m[0]]
		}
		// エスケープ解除は レス数を切り出した後に行う。sc は cp932 に無い文字を
		// &#129317; のような数値文字参照で送ってくる
		title := FixSCNotation(strings.TrimSpace(html.UnescapeString(rest)))
		out = append(out, Entry{ThreadKey: tk, Title: title, ResCount: res})
	}
	return out
}

// FixSCNotation は cp932 由来の文字を 5ch 側（JIS X 0208 由来）の表記へ寄せる。
//
// sc は Shift_JIS で配信されるため、Windows-31J の対応表でデコードすると
// 波ダッシュ等6文字が 5ch の UTF-8 ページと違うコードポイントになる。
// この差は NFKC でも吸収されない（U+FF5E→"~" / U+301C→U+301C）ため、
// 放置すると「同じ見た目なのに検索で一致しない」タイトルが混ざる。
//
// ★ 変換の向きは「sc → 5ch」。5ch 由来のデータが正であり、索引の大半はそちらで出来ている。
func FixSCNotation(s string) string {
	if !strings.ContainsAny(s, "～∥－￠￡￢") {
		return s
	}
	return scNotationRepl.Replace(s)
}

var scNotationRepl = strings.NewReplacer(
	"～", "〜", // ～ FULLWIDTH TILDE   → 〜 WAVE DASH
	"∥", "‖", // ∥ PARALLEL TO       → ‖ DOUBLE VERTICAL LINE
	"－", "−", // － FULLWIDTH HYPHEN  → − MINUS SIGN
	"￠", "¢", // ￠ FULLWIDTH CENT    → ¢ CENT SIGN
	"￡", "£", // ￡ FULLWIDTH POUND   → £ POUND SIGN
	"￢", "¬", // ￢ FULLWIDTH NOT     → ¬ NOT SIGN
)

// SCIndexURL は過去ログ索引ページの URL。base 例: https://tomcat.2ch.sc/livejupiter/kako/
func SCIndexURL(base string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base
}

// SCSubjectURL は倉庫の subject.txt の URL。
func SCSubjectURL(base string, no int) string {
	return fmt.Sprintf("%so%d/subject.txt", SCIndexURL(base), no)
}

// KakoNoOf は thread_key が属する倉庫番号（上4桁）を返す。
func KakoNoOf(threadKey int64) int { return int(threadKey / 1_000_000) }

// FetchKakoIndex は sc の過去ログ索引ページを取得して倉庫一覧を返す。
func (c *Client) FetchKakoIndex(base string) (dirs []KakoDir, notFound bool, err error) {
	body, ctype, notFound, err := c.fetch(SCIndexURL(base))
	if err != nil || notFound {
		return nil, notFound, err
	}
	return ParseKakoIndex(decodeHTML(body, ctype)), false, nil
}

// FetchSubject は1つの倉庫の subject.txt を取得して解析する。
func (c *Client) FetchSubject(base string, no int) (entries []Entry, notFound bool, err error) {
	body, ctype, notFound, err := c.fetch(SCSubjectURL(base, no))
	if err != nil || notFound {
		return nil, notFound, err
	}
	// subject.txt は charset 指定付きの text/plain で返るが、指定が無い板もありうる。
	// その場合 decodeHTML は UTF-8 扱いにするため、明示的に cp932 として読み直す。
	text := decodeHTML(body, ctype)
	if !strings.Contains(strings.ToLower(ctype), "charset=") {
		text = decodeHTML(body, "charset=shift_jis")
	}
	return ParseSubject(text), false, nil
}

// --- BBSMENU（板 → sc の板サーバー対応表） ---

// bbsmenu の <A HREF=//tomcat.2ch.sc/livejupiter/> 形式。
// 属性値は引用符で囲まれておらず、スキームも省略されている（プロトコル相対）。
var bbsMenuRe = regexp.MustCompile(`(?i)<a\s+href=["']?(?:https?:)?//([a-z0-9.\-]+\.2ch\.sc)/([a-z0-9_]+)/`)

// ParseBBSMenu は https://www.2ch.sc/bbsmenu.html から board_id → ホスト名を返す。
// 同じ板が複数回現れる場合は最初のものを採用する。
// 板以外のリンク（info / find など）も混ざるため、呼び出し側でこちらが持つ板と突き合わせること。
func ParseBBSMenu(page string) map[string]string {
	out := map[string]string{}
	for _, m := range bbsMenuRe.FindAllStringSubmatch(page, -1) {
		host, board := m[1], m[2]
		if _, ok := out[board]; !ok {
			out[board] = host
		}
	}
	return out
}

// FetchBBSMenu は bbsmenu を取得して board_id → ホスト名を返す。
func (c *Client) FetchBBSMenu(url string) (map[string]string, error) {
	body, ctype, notFound, err := c.fetch(url)
	if err != nil {
		return nil, err
	}
	if notFound {
		return nil, fmt.Errorf("%s: 404", url)
	}
	return ParseBBSMenu(decodeHTML(body, ctype)), nil
}
