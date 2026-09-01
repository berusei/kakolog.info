// Package scrape は 5ch 過去ログサーバーの kakoNNNN.html 一覧ページから
// 新規スレッドのメタデータ（thread_key / title / res_count）を取得する。
// dat（レス本文）には一切アクセスしない（仕様書1.2）。
//
// フロー（依頼者指定）:
//  1. board-urls.json の url + kako0000.html を開く（ページは新しい順に並ぶ）
//  2. ページ冒頭の「Latest update」を読み、前回スキャン時から進んでいなければ板ごとスキップ
//  3. DB 上の最新 thread_key（アンカー。日付バグスレ除外のため thread_key < 現在時刻）を探す
//  4. 0000 からインクリメントし、アンカーが見つかるまでページを進める
//  5. そこまでに取得済みのページ（= 追加分を含む）のエントリを DB へ投入する
//     （デクリメントでの再取得は不要。インクリメント時に内容を保持している）
//
// 404・更新なしの板はスキップする。
package scrape

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"

	"kakosearch/internal/docid"
)

// Entry は一覧ページの1スレッド分。
type Entry struct {
	ThreadKey int64
	Title     string
	ResCount  int64
}

// 例: <p class="main_odd">1<span class="filename">1785856507.dat</span>
//
//	<span class="title"><a href="/test/read.cgi/iPhone/1785856507/">タイトル </a></span>
//	<span class="lines">17</span></p>
var entryRe = regexp.MustCompile(
	`<span class="filename">(\d+)\.dat</span>\s*<span class="title"><a[^>]*>(.*?)</a></span>\s*<span class="lines">(\d+)</span>`)

// Parse は kako 一覧ページの HTML からスレッド一覧を抜き出す（ページ内の掲載順のまま）。
func Parse(page string) []Entry {
	ms := entryRe.FindAllStringSubmatch(page, -1)
	out := make([]Entry, 0, len(ms))
	for _, m := range ms {
		tk, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		res, _ := strconv.ParseInt(m[3], 10, 64)
		out = append(out, Entry{
			ThreadKey: tk,
			Title:     strings.TrimSpace(html.UnescapeString(m[2])),
			ResCount:  res,
		})
	}
	return out
}

// 一覧ページ冒頭に載っているページ自体の生成時刻。
//
//	<p><em class="total">Total: 2629 thread(s)</span> <span class="latest">Latest update: 2026/08/07 21:27:00</em></p>
//
// ★ 閉じタグが </em> になっている壊れたマークアップなので、</span> を当てにしてはならない。
// ラベル文字列（"Latest update:"）も板によって違いうるため、class="latest" の直後に
// 現れる最初の日時だけを拾う。値は Last-Modified ヘッダと一致する JST。
var latestRe = regexp.MustCompile(
	`class="latest">[^<]*?(\d{4})/(\d{1,2})/(\d{1,2})\s+(\d{1,2}):(\d{2}):(\d{2})`)

// ParseLatest は一覧ページの「Latest update」を返す。記載が無ければ ok=false。
func ParseLatest(page string) (t time.Time, ok bool) {
	m := latestRe.FindStringSubmatch(page)
	if m == nil {
		return time.Time{}, false
	}
	n := make([]int, 6)
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return time.Time{}, false
		}
		n[i] = v
	}
	return time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, docid.JST), true
}

var charsetRe = regexp.MustCompile(`(?i)charset=["']?([a-zA-Z0-9_\-]+)`)

// decodeHTML はレスポンスを UTF-8 文字列にする。charset は Content-Type ヘッダ →
// 冒頭2KBの meta の順で判定し、SJIS 系は cp932 相当として扱う（仕様書16.5。
// x/text の japanese.ShiftJIS は WHATWG 準拠で Windows-31J 互換）。不明時は UTF-8。
func decodeHTML(body []byte, contentType string) string {
	cs := ""
	if m := charsetRe.FindStringSubmatch(contentType); m != nil {
		cs = m[1]
	} else {
		head := body
		if len(head) > 2048 {
			head = head[:2048]
		}
		if m := charsetRe.FindSubmatch(head); m != nil {
			cs = string(m[1])
		}
	}
	switch strings.ToLower(cs) {
	case "shift_jis", "shift-jis", "sjis", "x-sjis", "windows-31j", "cp932", "ms932":
		if b, _, err := transform.Bytes(japanese.ShiftJIS.NewDecoder(), body); err == nil {
			return string(b)
		}
	case "euc-jp":
		if b, _, err := transform.Bytes(japanese.EUCJP.NewDecoder(), body); err == nil {
			return string(b)
		}
	}
	return string(body)
}

// Client は取得クライアント。単一 goroutine から使う想定（並列化しない。相手は他人のサーバー）。
type Client struct {
	HTTP      *http.Client
	UserAgent string
	Delay     time.Duration // リクエスト間の待機
	started   bool
}

func New(delay time.Duration) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		UserAgent: "kakosearch-scraper/1.0 (+nightly metadata sync)",
		Delay:     delay,
	}
}

// PageURL は一覧ページの URL を組み立てる。base 例: https://mi.5ch.io/news4vip/kako/
func PageURL(base string, page int) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return fmt.Sprintf("%skako%04d.html", base, page)
}

// Page は一覧ページ1枚分の解析結果。
type Page struct {
	Entries []Entry
	Latest  time.Time // ページ自身が申告する生成時刻。ゼロ値 = 記載が無かった
}

// FetchPage は1ページ取得して解析する。404/410 は (Page{}, true, nil)。
func (c *Client) FetchPage(base string, page int) (p Page, notFound bool, err error) {
	body, ctype, notFound, err := c.fetch(PageURL(base, page))
	if err != nil || notFound {
		return Page{}, notFound, err
	}
	doc := decodeHTML(body, ctype)
	out := Page{Entries: Parse(doc)}
	if t, ok := ParseLatest(doc); ok {
		out.Latest = t
	}
	return out, false, nil
}

// fetch は1 URL を取得して本文と Content-Type を返す。404/410 は notFound = true。
// ネットワークエラーと 5xx は3秒空けて1回だけ再試行する。
// 相手は他人のサーバーなので、呼び出しの間隔（Delay）はここで一元的に効かせる。
func (c *Client) fetch(url string) (body []byte, contentType string, notFound bool, err error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		} else if c.started && c.Delay > 0 {
			time.Sleep(c.Delay)
		}
		c.started = true

		req, rerr := http.NewRequest("GET", url, nil)
		if rerr != nil {
			return nil, "", false, rerr
		}
		req.Header.Set("User-Agent", c.UserAgent)
		resp, derr := c.HTTP.Do(req)
		if derr != nil {
			lastErr = derr
			continue
		}
		b, berr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if berr != nil {
			lastErr = berr
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return b, resp.Header.Get("Content-Type"), false, nil
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			return nil, "", true, nil
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		default:
			return nil, "", false, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
	}
	return nil, "", false, fmt.Errorf("%s: %w", url, lastErr)
}

// Result は1板分のスキャン結果。
type Result struct {
	Pages       int       // 取得できたページ数（0 = kako0000.html が 404、または Latest update でスキップ）
	Entries     []Entry   // 取得した全エントリ（thread_key で重複除去、新しい順）
	Latest      time.Time // kako0000.html が申告する生成時刻。ゼロ値 = 記載が無かった
	Skipped     bool      // Latest update が since から進んでいないため板ごとスキップした
	AnchorFound bool      // アンカー thread_key に到達した
	StoppedOld  bool      // アンカー自体は見つからないが、ページ全体が既知だったため打ち切った
	HitLimit    bool      // maxPages に達した
}

// ScanBoard は kako0000.html からアンカーが見つかるまでページを進め、
// そこまでの全エントリを返す。
//
// since は前回この板をスキャンしたときの Latest update。kako0000.html が申告する
// Latest update がそこから進んでいなければ、取得元に新着が無いことが確定するので
// 1リクエストだけで打ち切る（Skipped = true）。since がゼロ値のとき、および
// ページに Latest update の記載が無いときは、この門番は働かず必ず本スキャンへ進む。
//
// ★ この門番は「取りに行くかどうか」だけを決める。通過した後の判定（アンカー到達・
// 既知判定・重複除去）は従来どおり一切省略しない。Latest update が進んでいても
// 実際の新規が0件ということはありうる。
//
// アンカーのスレッドが一覧から消えている場合（削除等）の保険として、
// ページ内の全エントリが known（既に DB に存在）になった時点でも打ち切る。
// アンカーが 0（DB にその板の行が無い＝新規板）のときは末尾（404）まで全取得する。
func (c *Client) ScanBoard(base string, anchor int64, known func(int64) bool, maxPages int, since time.Time) (Result, error) {
	var res Result
	seen := map[int64]bool{}
	for page := 0; page < maxPages; page++ {
		p, notFound, err := c.FetchPage(base, page)
		if err != nil {
			return res, err
		}
		if notFound || len(p.Entries) == 0 {
			return res, nil // page=0 なら板ごとスキップ、それ以外は末尾到達
		}
		if page == 0 {
			res.Latest = p.Latest
			if !p.Latest.IsZero() && !since.IsZero() && !p.Latest.After(since) {
				res.Skipped = true
				return res, nil
			}
		}
		res.Pages++
		allKnown := true
		for _, e := range p.Entries {
			if e.ThreadKey == anchor {
				res.AnchorFound = true
			}
			if !known(e.ThreadKey) {
				allKnown = false
			}
			if !seen[e.ThreadKey] {
				seen[e.ThreadKey] = true
				res.Entries = append(res.Entries, e)
			}
		}
		if res.AnchorFound {
			return res, nil
		}
		if anchor > 0 && allKnown {
			res.StoppedOld = true
			return res, nil
		}
	}
	res.HitLimit = true
	return res, nil
}
