package scrape

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kakosearch/internal/docid"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

// 依頼者提供の実ページ断片（末尾スペース・絵文字を含む）
const sampleHTML = `
<p class="main_odd">1<span class="filename">1785856507.dat</span><span class="title"><a href="/test/read.cgi/iPhone/1785856507/">【モンスト】モンスターストライク総合8/5【ぽこあの🪦】 </a></span><span class="lines">17</span></p>
<p class="main_even">2<span class="filename">1785836290.dat</span><span class="title"><a href="/test/read.cgi/iPhone/1785836290/">【モンスト】モンスターストライク総合8/4【フェスバの🪦】 </a></span><span class="lines">1002</span></p>
`

func TestParse(t *testing.T) {
	got := Parse(sampleHTML)
	if len(got) != 2 {
		t.Fatalf("件数 = %d, want 2", len(got))
	}
	want0 := Entry{ThreadKey: 1785856507, Title: "【モンスト】モンスターストライク総合8/5【ぽこあの🪦】", ResCount: 17}
	if got[0] != want0 {
		t.Errorf("got[0] = %+v, want %+v", got[0], want0)
	}
	if got[1].ThreadKey != 1785836290 || got[1].ResCount != 1002 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestParseEntities(t *testing.T) {
	h := `<p class="main_odd">1<span class="filename">1700000000.dat</span><span class="title"><a href="/x/">A&amp;B &quot;C&quot;</a></span><span class="lines">5</span></p>`
	got := Parse(h)
	if len(got) != 1 || got[0].Title != `A&B "C"` {
		t.Fatalf("HTML実体参照の復元に失敗: %+v", got)
	}
}

func TestPageURL(t *testing.T) {
	if u := PageURL("https://mi.5ch.io/news4vip/kako/", 0); u != "https://mi.5ch.io/news4vip/kako/kako0000.html" {
		t.Errorf("PageURL = %s", u)
	}
	if u := PageURL("https://mi.5ch.io/news4vip/kako", 123); u != "https://mi.5ch.io/news4vip/kako/kako0123.html" {
		t.Errorf("PageURL（スラッシュ無し）= %s", u)
	}
}

func TestDecodeHTMLShiftJIS(t *testing.T) {
	src := `<span class="title"><a href="/x/">チーズケーキ</a></span>`
	sjis, _, err := transform.Bytes(japanese.ShiftJIS.NewEncoder(), []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// Content-Type ヘッダで判定するケース
	if got := decodeHTML(sjis, "text/html; charset=Shift_JIS"); got != src {
		t.Errorf("ヘッダ判定で復元失敗: %q", got)
	}
	// meta タグで判定するケース
	meta := append([]byte(`<meta http-equiv="Content-Type" content="text/html; charset=shift_jis">`), sjis...)
	if got := decodeHTML(meta, "text/html"); got[len(got)-len(src):] != src {
		t.Errorf("meta判定で復元失敗")
	}
	// 指定なし → UTF-8 のまま
	if got := decodeHTML([]byte(src), ""); got != src {
		t.Errorf("UTF-8素通しに失敗: %q", got)
	}
}

// 実ページの該当箇所そのまま。★閉じタグが </em> になっている（壊れたマークアップ）
const latestHTML = `<p><em class="total">Total: 2629 thread(s)</span> <span class="latest">Latest update: 2026/08/07 21:27:00</em></p>`

func TestParseLatest(t *testing.T) {
	got, ok := ParseLatest(latestHTML)
	if !ok {
		t.Fatal("Latest update を取れていない")
	}
	want := time.Date(2026, 8, 7, 21, 27, 0, 0, docid.JST)
	if !got.Equal(want) {
		t.Errorf("ParseLatest = %v, want %v", got, want)
	}
	// Last-Modified ヘッダ（Fri, 07 Aug 2026 12:27:00 GMT）と一致すること
	if u := got.UTC().Format("2006-01-02 15:04:05"); u != "2026-08-07 12:27:00" {
		t.Errorf("UTC 換算 = %s, want 2026-08-07 12:27:00", u)
	}
}

func TestParseLatestVariants(t *testing.T) {
	// ラベルが違っても class="latest" 直後の日時を拾えること
	if got, ok := ParseLatest(`<span class="latest">最終更新 2024/1/2 3:04:05</span>`); !ok ||
		!got.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, docid.JST)) {
		t.Errorf("1桁の月日時に対応できていない: %v %v", got, ok)
	}
	// 記載が無いページ（旧形式）は ok=false。門番は働かず本スキャンへ進む
	if _, ok := ParseLatest(`<p>Total: 10 thread(s)</p>`); ok {
		t.Error("記載が無いのに ok=true")
	}
	// class="latest" 以外の場所にある日時を拾わないこと
	if _, ok := ParseLatest(`<span class="other">2020/01/01 00:00:00</span>`); ok {
		t.Error("無関係な日時を拾っている")
	}
}

func TestScanBoardSkipsWhenLatestNotAdvanced(t *testing.T) {
	// Latest update が前回スキャン時と同じ → 1リクエストで打ち切り、page1 は引かない
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/kako0000.html", func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, latestHTML+entryHTML(300, "新規", 1))
	})
	mux.HandleFunc("/kako0001.html", func(w http.ResponseWriter, r *http.Request) {
		t.Error("スキップされるはずの板で2ページ目を取得した")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	since := time.Date(2026, 8, 7, 21, 27, 0, 0, docid.JST)
	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(int64) bool { return false }, 10, since)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Pages != 0 || len(res.Entries) != 0 {
		t.Fatalf("スキップされていない: %+v", res)
	}
	if !res.Latest.Equal(since) {
		t.Errorf("Latest = %v, want %v", res.Latest, since)
	}
	if hits != 1 {
		t.Errorf("リクエスト数 = %d, want 1", hits)
	}
}

func TestScanBoardScansWhenLatestAdvanced(t *testing.T) {
	// Latest update が1秒でも進んでいれば、従来どおりアンカーまでの本スキャンを行う
	srv := newTestServer(t, map[int]string{
		0: latestHTML + entryHTML(300, "新規", 1) + entryHTML(100, "既知アンカー", 2),
	})
	defer srv.Close()

	since := time.Date(2026, 8, 7, 21, 26, 59, 0, docid.JST)
	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(tk int64) bool { return tk == 100 }, 10, since)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || !res.AnchorFound || res.Pages != 1 || len(res.Entries) != 2 {
		t.Fatalf("本スキャンが走っていない: %+v", res)
	}
}

func TestScanBoardNoGateWithoutState(t *testing.T) {
	// since がゼロ値（初回・記録なし）→ Latest update があってもスキップしない
	srv := newTestServer(t, map[int]string{
		0: latestHTML + entryHTML(300, "新規", 1) + entryHTML(100, "既知", 2),
	})
	defer srv.Close()

	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(tk int64) bool { return tk == 100 }, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.Pages != 1 {
		t.Fatalf("初回はスキップしてはならない: %+v", res)
	}
}

func TestScanBoardNoGateWithoutLatestOnPage(t *testing.T) {
	// ページに Latest update の記載が無い → since があっても門番は働かない
	srv := newTestServer(t, map[int]string{
		0: entryHTML(300, "新規", 1) + entryHTML(100, "既知", 2),
	})
	defer srv.Close()

	since := time.Date(2030, 1, 1, 0, 0, 0, 0, docid.JST) // 未来
	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(tk int64) bool { return tk == 100 }, 10, since)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.Pages != 1 || !res.Latest.IsZero() {
		t.Fatalf("記載が無い板はスキップしてはならない: %+v", res)
	}
}

// entryHTML はテスト用の1行を生成する。
func entryHTML(tk int64, title string, lines int) string {
	return fmt.Sprintf(`<p class="main_odd">1<span class="filename">%d.dat</span><span class="title"><a href="/t/%d/">%s</a></span><span class="lines">%d</span></p>`+"\n", tk, tk, title, lines)
}

func newTestServer(t *testing.T, pages map[int]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for p, body := range pages {
		b := body
		mux.HandleFunc(fmt.Sprintf("/kako%04d.html", p), func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, b)
		})
	}
	return httptest.NewServer(mux)
}

func TestScanBoardAnchorOnFirstPage(t *testing.T) {
	// page0: 新規2件 + アンカー。1リクエストで停止すること
	srv := newTestServer(t, map[int]string{
		0: entryHTML(300, "新規B", 10) + entryHTML(200, "新規A", 20) + entryHTML(100, "既知", 500),
	})
	defer srv.Close()

	c := New(0)
	known := func(tk int64) bool { return tk == 100 }
	res, err := c.ScanBoard(srv.URL+"/", 100, known, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AnchorFound || res.Pages != 1 || len(res.Entries) != 3 {
		t.Fatalf("res = %+v", res)
	}
	if res.Entries[0].ThreadKey != 300 {
		t.Errorf("新しい順になっていない: %+v", res.Entries)
	}
}

func TestScanBoardAnchorOnSecondPage(t *testing.T) {
	srv := newTestServer(t, map[int]string{
		0: entryHTML(500, "新規C", 1) + entryHTML(400, "新規B", 2),
		1: entryHTML(300, "新規A", 3) + entryHTML(100, "既知アンカー", 4),
	})
	defer srv.Close()

	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(tk int64) bool { return tk == 100 }, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AnchorFound || res.Pages != 2 || len(res.Entries) != 4 {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanBoardAnchorDeleted(t *testing.T) {
	// アンカー(250)が一覧から消えている → 全既知ページで打ち切る保険が働くこと
	srv := newTestServer(t, map[int]string{
		0: entryHTML(300, "新規", 1),
		1: entryHTML(200, "既知1", 2) + entryHTML(150, "既知2", 3),
	})
	defer srv.Close()

	c := New(0)
	known := func(tk int64) bool { return tk == 200 || tk == 150 }
	res, err := c.ScanBoard(srv.URL+"/", 250, known, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AnchorFound || !res.StoppedOld || res.Pages != 2 || len(res.Entries) != 3 {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanBoardNewBoardToEnd(t *testing.T) {
	// anchor=0（新規板）→ 404 まで全ページ取得
	srv := newTestServer(t, map[int]string{
		0: entryHTML(300, "c", 1),
		1: entryHTML(200, "b", 2),
		2: entryHTML(100, "a", 3),
	})
	defer srv.Close()

	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 0, func(int64) bool { return false }, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 3 || len(res.Entries) != 3 || res.AnchorFound || res.HitLimit {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanBoard404(t *testing.T) {
	srv := newTestServer(t, map[int]string{}) // kako0000.html が存在しない
	defer srv.Close()

	c := New(0)
	res, err := c.ScanBoard(srv.URL+"/", 100, func(int64) bool { return false }, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 0 || len(res.Entries) != 0 {
		t.Fatalf("404板はスキップされるべき: %+v", res)
	}
}
