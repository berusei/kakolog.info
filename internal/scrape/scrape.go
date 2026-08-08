// Package scrape は 5ch 過去ログサーバーの kakoNNNN.html 一覧ページから
// 新規スレッドのメタデータ（thread_key / title / res_count）を取得する。
// dat（レス本文）には一切アクセスしない（仕様書1.2）。
//
// フロー（依頼者指定）:
//  1. board-urls.json の url + kako0000.html を開く（ページは新しい順に並ぶ）
//  2. DB 上の最新 thread_key（アンカー。日付バグスレ除外のため thread_key < 現在時刻）を探す
//  3. 0000 からインクリメントし、アンカーが見つかるまでページを進める
//  4. そこまでに取得済みのページ（= 追加分を含む）のエントリを DB へ投入する
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
)

// Entry は一覧ページの1スレッド分。
type Entry struct {
	ThreadKey int64
	Title     string
	ResCount  int64
}

// 例: <p class="main_odd">1<span class="filename">1785856507.dat</span>
//     <span class="title"><a href="/test/read.cgi/iPhone/1785856507/">タイトル </a></span>
//     <span class="lines">17</span></p>
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

// FetchPage は1ページ取得して解析する。404/410 は (nil, true, nil)。
// ネットワークエラーと 5xx は3秒空けて1回だけ再試行する。
func (c *Client) FetchPage(base string, page int) (entries []Entry, notFound bool, err error) {
	url := PageURL(base, page)
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
			return nil, false, rerr
		}
		req.Header.Set("User-Agent", c.UserAgent)
		resp, derr := c.HTTP.Do(req)
		if derr != nil {
			lastErr = derr
			continue
		}
		body, berr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if berr != nil {
			lastErr = berr
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return Parse(decodeHTML(body, resp.Header.Get("Content-Type"))), false, nil
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			return nil, true, nil
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		default:
			return nil, false, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
	}
	return nil, false, fmt.Errorf("%s: %w", url, lastErr)
}

// Result は1板分のスキャン結果。
type Result struct {
	Pages       int     // 取得できたページ数（0 = kako0000.html が 404）
	Entries     []Entry // 取得した全エントリ（thread_key で重複除去、新しい順）
	AnchorFound bool    // アンカー thread_key に到達した
	StoppedOld  bool    // アンカー自体は見つからないが、ページ全体が既知だったため打ち切った
	HitLimit    bool    // maxPages に達した
}

// ScanBoard は kako0000.html からアンカーが見つかるまでページを進め、
// そこまでの全エントリを返す。
//
// アンカーのスレッドが一覧から消えている場合（削除等）の保険として、
// ページ内の全エントリが known（既に DB に存在）になった時点でも打ち切る。
// アンカーが 0（DB にその板の行が無い＝新規板）のときは末尾（404）まで全取得する。
func (c *Client) ScanBoard(base string, anchor int64, known func(int64) bool, maxPages int) (Result, error) {
	var res Result
	seen := map[int64]bool{}
	for page := 0; page < maxPages; page++ {
		entries, notFound, err := c.FetchPage(base, page)
		if err != nil {
			return res, err
		}
		if notFound || len(entries) == 0 {
			return res, nil // page=0 なら板ごとスキップ、それ以外は末尾到達
		}
		res.Pages++
		allKnown := true
		for _, e := range entries {
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
