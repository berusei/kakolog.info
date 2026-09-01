package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"kakosearch/internal/scrape"
	"kakosearch/internal/store"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE threads (
		id INTEGER PRIMARY KEY, board_id TEXT NOT NULL, thread_key INTEGER NOT NULL,
		title TEXT NOT NULL, res_count INTEGER NOT NULL);
		CREATE UNIQUE INDEX idx_threads_lookup ON threads(board_id, thread_key)`); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureScrapeState(db); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSCState(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertThread(t *testing.T, db *sql.DB, board string, tk int64, title string, res int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES (?, ?, ?, ?)`,
		board, tk, title, res); err != nil {
		t.Fatal(err)
	}
}

func kakoPage(latest string, rows string) string {
	return `<html><body><p><em class="total">Total: 3 thread(s)</span> ` +
		`<span class="latest">Latest update: ` + latest + `</em></p>` + rows + `</body></html>`
}

func kakoRow(tk int64, title string, res int) string {
	return `<p class="main_odd">1<span class="filename">` +
		strconv.FormatInt(tk, 10) + `.dat</span><span class="title"><a href="/test/read.cgi/livejupiter/` +
		strconv.FormatInt(tk, 10) + `/">` + title + ` </a></span><span class="lines">` + strconv.Itoa(res) + `</span></p>`
}

// ★ 中核の保証（docs/changes/2026-08-21.md の設計判断4）:
// 5ch の倉庫が復旧したら、sc から補完した行は title ごと 5ch の値へ寄せて収束する。
// 5ch 由来の既存行の title は従来どおり書き換えない（仕様書9.3）。
func TestScrapeBoardConvergesSCSourcedRows(t *testing.T) {
	db := newTestDB(t)
	const board = "livejupiter"
	now := time.Now()

	// 停止前に 5ch から入った行（アンカー）
	insertThread(t, db, board, 1786101654, "5ch由来のスレ", 10)
	// 停止中に sc から補完した行。表記もレス数も sc のもの
	insertThread(t, db, board, 1786500000, "チーズ～ケーキ", 5)
	if err := store.MarkSCSourced(db, board, 1786500000, now); err != nil {
		t.Fatal(err)
	}

	// 復旧した 5ch の一覧。sc 由来スレの正しい表記とレス数、および新規スレを含む
	page := kakoPage("2026/08/21 03:00:00",
		kakoRow(1786900000, "復旧後の新規スレ", 3)+
			kakoRow(1786500000, "チーズ〜ケーキ", 987)+
			kakoRow(1786101654, "5ch由来のスレ", 10))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/kako0000.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	anchorStmt, _ := db.Prepare(`SELECT COALESCE(MAX(thread_key), 0) FROM threads WHERE board_id = ? AND thread_key < ?`)
	defer anchorStmt.Close()
	existsStmt, _ := db.Prepare(`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	defer existsStmt.Close()

	bu := boardURL{BoardID: board, URL: srv.URL + "/"}
	// ★ アンカーは sc 由来の 1786500000 ではなく、5ch で最後に見た 1786101654。
	// これがあるおかげで、復旧後の走査が停止期間ぶんを丸ごと拾い直せる
	st := store.BoardState{Anchor5ch: 1786101654}
	res, err := scrapeBoard(db, scrape.New(0), anchorStmt, existsStmt, bu, 10, false, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.nNew != 1 {
		t.Errorf("新規 = %d, want 1", res.nNew)
	}

	var title string
	var rc int64
	if err := db.QueryRow(`SELECT title, res_count FROM threads WHERE board_id=? AND thread_key=?`,
		board, int64(1786500000)).Scan(&title, &rc); err != nil {
		t.Fatal(err)
	}
	if title != "チーズ〜ケーキ" {
		t.Errorf("sc由来行の title = %q, want %q（5chの値で上書きされていない）", title, "チーズ〜ケーキ")
	}
	if rc != 987 {
		t.Errorf("sc由来行の res_count = %d, want 987", rc)
	}

	// 裏が取れたので sc_sourced からは消える
	n, err := store.CountSCSourced(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sc_sourced が %d件残っている（5chで取り直した行は消えるべき）", n)
	}

	// 新規スレが入り、アンカーが進んでいること
	if err := db.QueryRow(`SELECT title FROM threads WHERE board_id=? AND thread_key=?`,
		board, int64(1786900000)).Scan(&title); err != nil {
		t.Fatalf("新規スレが入っていない: %v", err)
	}
	state, _ := store.LoadScrapeState(db)
	if state[board].Anchor5ch != 1786900000 {
		t.Errorf("anchor_5ch = %d, want 1786900000", state[board].Anchor5ch)
	}
}

// 5ch 由来の既存行は、レス数が変わっても title を書き換えない（仕様書9.3）。
func TestScrapeBoardKeepsTitleOfFiveChRows(t *testing.T) {
	db := newTestDB(t)
	const board = "livejupiter"
	insertThread(t, db, board, 1786101654, "元のタイトル", 10)

	page := kakoPage("2026/08/21 03:00:00", kakoRow(1786101654, "書き換わったタイトル", 42))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	anchorStmt, _ := db.Prepare(`SELECT COALESCE(MAX(thread_key), 0) FROM threads WHERE board_id = ? AND thread_key < ?`)
	defer anchorStmt.Close()
	existsStmt, _ := db.Prepare(`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	defer existsStmt.Close()

	if _, err := scrapeBoard(db, scrape.New(0), anchorStmt, existsStmt,
		boardURL{BoardID: board, URL: srv.URL + "/"}, 10, false,
		store.BoardState{Anchor5ch: 1786101654}); err != nil {
		t.Fatal(err)
	}
	var title string
	var rc int64
	db.QueryRow(`SELECT title, res_count FROM threads WHERE board_id=? AND thread_key=?`,
		board, int64(1786101654)).Scan(&title, &rc)
	if title != "元のタイトル" {
		t.Errorf("title = %q（5ch由来行の title を書き換えてはならない）", title)
	}
	if rc != 42 {
		t.Errorf("res_count = %d, want 42（レス数は更新される）", rc)
	}
}

// bootstrapAnchors は sc から1行も入れる前に 5ch 用アンカーを確定させる。
// これを飛ばすと sc 由来の新しい thread_key で MAX が飛び、復旧後に停止期間が埋まらなくなる。
func TestBootstrapAnchors(t *testing.T) {
	db := newTestDB(t)
	insertThread(t, db, "livejupiter", 1786101654, "5ch由来", 10)
	insertThread(t, db, "news4vip", 1786000000, "5ch由来", 10)
	insertThread(t, db, "emptyboard", 2_500_000_000, "未来日付の壊れた行", 1)

	anchorStmt, _ := db.Prepare(`SELECT COALESCE(MAX(thread_key), 0) FROM threads WHERE board_id = ? AND thread_key < ?`)
	defer anchorStmt.Close()

	urls := []boardURL{{BoardID: "livejupiter"}, {BoardID: "news4vip"}, {BoardID: "emptyboard"}}
	state, _ := store.LoadScrapeState(db)
	n, err := bootstrapAnchors(db, anchorStmt, urls, nil, state, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("初期化した板数 = %d, want 2（未来日付しか無い板は対象外）", n)
	}
	saved, _ := store.LoadScrapeState(db)
	if saved["livejupiter"].Anchor5ch != 1786101654 {
		t.Errorf("livejupiter anchor = %d", saved["livejupiter"].Anchor5ch)
	}
	if saved["emptyboard"].Anchor5ch != 0 {
		t.Errorf("emptyboard anchor = %d, want 0", saved["emptyboard"].Anchor5ch)
	}

	// 2回目は何もしない（既に記録済みの板を上書きしない）
	state2, _ := store.LoadScrapeState(db)
	if n2, _ := bootstrapAnchors(db, anchorStmt, urls, nil, state2, false); n2 != 0 {
		t.Errorf("2回目の初期化数 = %d, want 0", n2)
	}
}

// --- sc 補完側（scrapesc.go） ---

// scTestServer は sc の過去ログ倉庫を模す。倉庫番号 → subject.txt の中身。
func scTestServer(t *testing.T, counts map[int]int, subjects map[int]string) *httptest.Server {
	t.Helper()
	nos := make([]int, 0, len(counts))
	for no := range counts {
		nos = append(nos, no)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nos))) // 索引ページは新しい順
	index := "<TABLE BORDER=2>"
	for _, no := range nos {
		s := strconv.Itoa(no)
		index += `<tr><td><a target="_blank" href="o` + s + `/">#b/` + s + `</a></td>` +
			`<td align="right">` + strconv.Itoa(counts[no]) + `</td><td align="right">1.0</td>` +
			`<td align="right"><a href="o` + s + `/subject.txt">subject.txt</a></td></tr>`
	}
	index += "</TABLE>"

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b/kako/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(index))
			return
		}
		for no, body := range subjects {
			if r.URL.Path == "/b/kako/o"+strconv.Itoa(no)+"/subject.txt" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
}

func TestScrapeBoardSC(t *testing.T) {
	db := newTestDB(t)
	const board = "b"
	// 停止直前まで 5ch から入っている状態。アンカーの倉庫は 1786
	insertThread(t, db, board, 1786100000, "5ch由来のスレ", 500)

	srv := scTestServer(t,
		map[int]int{1787: 2, 1786: 2, 1785: 3}, // 1785 は停止期間より前 → 初回は取りに行かない
		map[int]string{
			1787: "1787000001.dat<>停止中に立ったスレ (12)\n1787000002.dat<>もう1つ (3)\n",
			1786: "1786900000.dat<>停止直後のスレ (7)\n1786100000.dat<>5ch由来のスレ (400)\n",
			1785: "1785000001.dat<>もっと古いスレ (1)\n",
		})
	defer srv.Close()

	existsStmt, _ := db.Prepare(`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	defer existsStmt.Close()
	base := srv.URL + "/b/kako/"

	res, err := scrapeBoardSC(db, scrape.New(0), existsStmt, board, base, 1786100000, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.nNew != 3 {
		t.Errorf("新規 = %d, want 3（o1787の2件 + o1786の1件）", res.nNew)
	}
	if res.dirsFetched != 2 {
		t.Errorf("取得した倉庫 = %d, want 2（o1785 は初回の対象外）", res.dirsFetched)
	}

	// ★ 5ch 由来の行に sc のレス数を被せない（実測13%が少ない方へ退行するため）
	var rc int64
	db.QueryRow(`SELECT res_count FROM threads WHERE board_id=? AND thread_key=?`,
		board, int64(1786100000)).Scan(&rc)
	if rc != 500 {
		t.Errorf("5ch由来行の res_count = %d, want 500（scの400で上書きしてはならない）", rc)
	}
	// o1785 の中身は入っていない
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM threads WHERE thread_key = 1785000001`).Scan(&n)
	if n != 0 {
		t.Errorf("停止期間より前の倉庫から取り込んでいる")
	}
	// 取りに行かなかった倉庫も格納数は基準値として記録し、次回に変化を検出できるようにする
	counts, _ := store.LoadSCKakoCounts(db, board)
	if counts[1785] != 3 {
		t.Errorf("o1785 の格納数が記録されていない: %v", counts)
	}
	keys, _ := store.SCSourcedKeys(db, board)
	if len(keys) != 3 || !keys[1787000001] {
		t.Errorf("sc_sourced = %v", keys)
	}

	// 2回目: 格納数が変わっていないので subject.txt を1つも取らない
	res2, err := scrapeBoardSC(db, scrape.New(0), existsStmt, board, base, 1786100000, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.dirsFetched != 0 || res2.nNew != 0 {
		t.Errorf("2回目: 取得%d件 新規%d件, want 0件（格納数が同じ倉庫は取りに行かない）",
			res2.dirsFetched, res2.nNew)
	}
	if res2.dirsSkipped != 3 {
		t.Errorf("2回目のスキップ = %d, want 3", res2.dirsSkipped)
	}
}

// ★ 倉庫番号はスレの作成時刻であってアーカイブ時刻ではない。
// 古い倉庫に後からスレが追加された場合、格納数の変化でしか検出できない。
func TestScrapeBoardSCDetectsOldDirGrowth(t *testing.T) {
	db := newTestDB(t)
	const board = "b"
	insertThread(t, db, board, 1786100000, "5ch由来のスレ", 500)
	existsStmt, _ := db.Prepare(`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	defer existsStmt.Close()

	// 1回目: o1780 は対象外だが格納数1を記録する
	srv1 := scTestServer(t, map[int]int{1786: 1, 1780: 1},
		map[int]string{1786: "1786900000.dat<>停止直後のスレ (7)\n", 1780: "1780000001.dat<>古いスレ (1)\n"})
	base1 := srv1.URL + "/b/kako/"
	if _, err := scrapeBoardSC(db, scrape.New(0), existsStmt, board, base1, 1786100000, 20, false); err != nil {
		t.Fatal(err)
	}
	srv1.Close()

	// 2回目: 長寿スレがアーカイブされ o1780 の格納数が増えた
	srv2 := scTestServer(t, map[int]int{1786: 1, 1780: 2},
		map[int]string{
			1786: "1786900000.dat<>停止直後のスレ (7)\n",
			1780: "1780000002.dat<>後から入った長寿スレ (1001)\n1780000001.dat<>古いスレ (1)\n",
		})
	defer srv2.Close()
	res, err := scrapeBoardSC(db, scrape.New(0), existsStmt, board, srv2.URL+"/b/kako/", 1786100000, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.dirsFetched != 1 {
		t.Errorf("取得した倉庫 = %d, want 1（格納数が変わった o1780 のみ）", res.dirsFetched)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM threads WHERE thread_key = 1780000002`).Scan(&n)
	if n != 1 {
		t.Error("古い倉庫に後から追加されたスレを取りこぼしている")
	}
}

// 上限に達した倉庫は記録を残さず、次回に持ち越す。
func TestScrapeBoardSCRespectsMaxDirs(t *testing.T) {
	db := newTestDB(t)
	const board = "b"
	insertThread(t, db, board, 1785000000, "5ch由来のスレ", 1)
	existsStmt, _ := db.Prepare(`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	defer existsStmt.Close()

	srv := scTestServer(t, map[int]int{1787: 1, 1786: 1, 1785: 1},
		map[int]string{
			1787: "1787000001.dat<>A (1)\n",
			1786: "1786000001.dat<>B (1)\n",
			1785: "1785000001.dat<>C (1)\n",
		})
	defer srv.Close()

	res, err := scrapeBoardSC(db, scrape.New(0), existsStmt, board, srv.URL+"/b/kako/", 1785000000, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.dirsFetched != 1 {
		t.Fatalf("取得した倉庫 = %d, want 1", res.dirsFetched)
	}
	counts, _ := store.LoadSCKakoCounts(db, board)
	if len(counts) != 1 || counts[1787] != 1 {
		t.Errorf("繰り越したぶんの格納数を記録している: %v（次回取りに行けなくなる）", counts)
	}
}
