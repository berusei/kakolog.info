package scrape

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
	res, err := c.ScanBoard(srv.URL+"/", 100, known, 10)
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
	res, err := c.ScanBoard(srv.URL+"/", 100, func(tk int64) bool { return tk == 100 }, 10)
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
	res, err := c.ScanBoard(srv.URL+"/", 250, known, 10)
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
	res, err := c.ScanBoard(srv.URL+"/", 0, func(int64) bool { return false }, 10)
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
	res, err := c.ScanBoard(srv.URL+"/", 100, func(int64) bool { return false }, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 0 || len(res.Entries) != 0 {
		t.Fatalf("404板はスキップされるべき: %+v", res)
	}
}
