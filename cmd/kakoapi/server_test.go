package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kakosearch/internal/config"
	"kakosearch/internal/docid"
	"kakosearch/internal/index"
	"kakosearch/internal/store"
	"kakosearch/internal/tokenizer"
)

// fakeSearch は Manticore の代役。受け取った Request を記録する。
type fakeSearch struct {
	lastReq index.Request
	result  *index.Result
	err     error
}

func (f *fakeSearch) Search(_ context.Context, req index.Request) (*index.Result, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}
func (f *fakeSearch) Ping(context.Context) error { return f.err }

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		`CREATE TABLE threads (id INTEGER PRIMARY KEY, board_id TEXT NOT NULL,
		   thread_key INTEGER NOT NULL, title TEXT NOT NULL, res_count INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX idx_threads_lookup ON threads(board_id, thread_key)`,
		`CREATE TABLE boards (board_idx INTEGER PRIMARY KEY, board_id TEXT NOT NULL UNIQUE,
		   board_name TEXT, category TEXT, thread_count INTEGER DEFAULT 0)`,
		`INSERT INTO boards VALUES (0, 'livejupiter', 'なんでも実況J', '', 2),
		   (1, 'news4vip', 'ニュー速VIP', '', 1)`,
		`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES
		   ('livejupiter', 1689063651, 'チーズケーキ食べたい', 1001),
		   ('livejupiter', 1689063700, '除外されるスレ', 5),
		   ('news4vip', 1600000000, 'ドラゴン', 100)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Stamp(db, tokenizer.Version, time.Date(2026, 8, 1, 3, 0, 0, 0, docid.JST)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO excluded_threads (board_id, thread_key) VALUES ('livejupiter', 1689063700)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func newTestServer(t *testing.T, fs *fakeSearch) *Server {
	t.Helper()
	cfg := config.Config{FiveChBase: "https://kako.5ch.io", HasOld: true, OldMaxRangeYears: 2}
	s, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, s *Server, url string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("JSONでないレスポンス (%d): %s", w.Code, w.Body.String())
	}
	return w.Code, body
}

func errCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func TestValidation(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{}}
	s := newTestServer(t, fs)
	cases := []struct {
		url    string
		status int
		code   string
	}{
		{"/api/search", 400, "empty_query"},
		{"/api/search?q=", 400, "empty_query"},
		{"/api/search?q=%20%20", 400, "empty_query"},
		{"/api/search?q=-%E3%81%82", 400, "empty_query"}, // 除外語のみ
		{"/api/search?q=%E3%81%AE", 400, "need_filter"},  // 1文字・絞り込みなし
		{"/api/search?q=%E3%81%82&per=9999", 400, "invalid_per"},
		{"/api/search?q=%E3%81%82&page=99999", 400, "page_limit"},
		{"/api/search?q=%E3%81%82&page=21", 400, "page_limit"},
		{"/api/search?q=%E3%81%82&page=0", 400, "invalid_page"},
		{"/api/search?q=%E3%81%82&sort=bad", 400, "invalid_sort"},
		{"/api/search?q=%E3%81%82&board=notexist", 400, "unknown_board"},
		{"/api/search?q=%E3%81%82&from=2023-13", 400, "invalid_date"},
		{"/api/search?q=%E3%81%82&from=abc", 400, "invalid_date"},
		{"/api/search?q=%E3%81%82&from=2024-01&to=2023-01", 400, "invalid_date"},
	}
	for _, c := range cases {
		status, body := get(t, s, c.url)
		if status != c.status || errCode(body) != c.code {
			t.Errorf("%s -> %d %q, want %d %q", c.url, status, errCode(body), c.status, c.code)
		}
	}
	// エラーメッセージは日本語（仕様書10.4）
	_, body := get(t, s, "/api/search?q=%E3%81%AE")
	msg := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "板または期間") {
		t.Errorf("案内メッセージが不適切: %s", msg)
	}
}

func TestOneCharWithFilterAllowed(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{}}
	s := newTestServer(t, fs)
	for _, u := range []string{
		"/api/search?q=%E3%81%AE&board=livejupiter",
		"/api/search?q=%E3%81%AE&from=2023-01",
		"/api/search?q=%E3%81%AE&to=2023-01",
	} {
		if status, body := get(t, s, u); status != 200 {
			t.Errorf("%s -> %d %v", u, status, body)
		}
	}
}

func TestSearchResponse(t *testing.T) {
	tk := int64(1689063651)
	fs := &fakeSearch{result: &index.Result{
		IDs:        []int64{docid.New(tk, 0), docid.New(1600000000, 1)},
		TotalFound: 120,
	}}
	s := newTestServer(t, fs)
	status, body := get(t, s, "/api/search?q=%E3%83%81%E3%83%BC%E3%82%BA&per=50")
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	if !strings.Contains(fs.lastReq.Match, `"チ ー ズ"`) {
		t.Errorf("MATCH が不正: %q", fs.lastReq.Match)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d 件", len(items))
	}
	it := items[0].(map[string]any)
	if it["title"] != "チーズケーキ食べたい" || it["res_count"].(float64) != 1001 {
		t.Errorf("item[0] = %v", it)
	}
	if it["thread_key"] != "1689063651" {
		t.Errorf("thread_key は文字列で %q", it["thread_key"])
	}
	if it["url"] != "https://kako.5ch.io/test/read.cgi/livejupiter/1689063651/" {
		t.Errorf("url = %v", it["url"])
	}
	if it["created_at"] != "2023-07-11T17:20:51+09:00" {
		t.Errorf("created_at = %v", it["created_at"])
	}
	if body["total"].(float64) != 120 || body["total_is_approximate"].(bool) {
		t.Errorf("total 系が不正: %v %v", body["total"], body["total_is_approximate"])
	}
	if body["max_page"].(float64) != 3 {
		t.Errorf("max_page = %v, want 3", body["max_page"])
	}
	if !strings.Contains(body["index_updated_at"].(string), "+09:00") {
		t.Errorf("index_updated_at が JST でない: %v", body["index_updated_at"])
	}
}

func TestApproximateTotalAndMaxPageCap(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{TotalFound: 1000, Approximate: true}}
	s := newTestServer(t, fs)
	_, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84")
	if !body["total_is_approximate"].(bool) {
		t.Error("cutoff 到達時に total_is_approximate が false")
	}
	if body["max_page"].(float64) != 20 {
		t.Errorf("max_page = %v, want 20（上限。仕様書12.1）", body["max_page"])
	}
}

func TestExclusionFilter(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{
		IDs:        []int64{docid.New(1689063700, 0), docid.New(1689063651, 0)},
		TotalFound: 2,
	}}
	s := newTestServer(t, fs)
	_, body := get(t, s, "/api/search?q=%E3%82%B9%E3%83%AC")
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("除外リストが効いていない: %d 件", len(items))
	}
	if items[0].(map[string]any)["thread_key"] != "1689063651" {
		t.Error("除外対象が残っている")
	}
}

func TestSortSelectsIndex(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{}}
	s := newTestServer(t, fs)
	get(t, s, "/api/search?q=%E3%81%82%E3%81%84&sort=new")
	if fs.lastReq.SortOld {
		t.Error("sort=new で SortOld=true")
	}
	get(t, s, "/api/search?q=%E3%81%82%E3%81%84&sort=old")
	if !fs.lastReq.SortOld {
		t.Error("sort=old で SortOld=false")
	}
}

func TestPeriodPropagation(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{}}
	s := newTestServer(t, fs)
	get(t, s, "/api/search?q=%E3%81%82%E3%81%84&from=2023-01&to=2023-01")
	f, tto, _ := docid.MonthBounds("2023-01")
	if fs.lastReq.TsFrom != f || fs.lastReq.TsTo != tto {
		t.Errorf("期間伝搬が不正: %d %d, want %d %d", fs.lastReq.TsFrom, fs.lastReq.TsTo, f, tto)
	}
}

func TestSearchdDown503(t *testing.T) {
	fs := &fakeSearch{err: errors.New("connection refused")}
	s := newTestServer(t, fs)
	status, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84")
	if status != 503 || errCode(body) != "search_unavailable" {
		t.Errorf("searchd 停止時 -> %d %q, want 503 search_unavailable", status, errCode(body))
	}
	// health も 503
	status, _ = get(t, s, "/api/health")
	if status != 503 {
		t.Errorf("health -> %d, want 503", status)
	}
}

func TestReservedCharsNoSyntaxError(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{}}
	s := newTestServer(t, fs)
	// "！(2) -@x ~" のような記号まみれのクエリが 200 で通る（仕様書14）
	status, _ := get(t, s, "/api/search?q="+
		"%EF%BC%81%28%EF%BC%92%29+%2D%40x&board=livejupiter")
	if status != 200 {
		t.Errorf("記号クエリ -> %d, want 200", status)
	}
	if !strings.Contains(fs.lastReq.Match, `\!`) {
		t.Errorf("エスケープされていない: %q", fs.lastReq.Match)
	}
}

func TestFallbackOldWithoutIndex(t *testing.T) {
	// kako_old が無い構成（仕様書12.3の縮退運用）
	tks := []int64{1672600000, 1672500000} // kako_new 順（新→旧）
	fs := &fakeSearch{result: &index.Result{
		IDs: []int64{docid.New(tks[0], 0), docid.New(tks[1], 0)}, TotalFound: 2}}
	cfg := config.Config{FiveChBase: "https://x", HasOld: false, OldMaxRangeYears: 2}
	s, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}

	// 期間なし → 400
	status, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84&sort=old")
	if status != 400 || errCode(body) != "need_range" {
		t.Errorf("縮退時の期間必須が効いていない: %d %q", status, errCode(body))
	}
	// 期間広すぎ → 400
	status, body = get(t, s, "/api/search?q=%E3%81%82%E3%81%84&sort=old&from=2020-01&to=2023-12")
	if status != 400 || errCode(body) != "range_too_wide" {
		t.Errorf("期間幅上限が効いていない: %d %q", status, errCode(body))
	}
	// 正常系: kako_new を引き、結果が反転される
	threads := [][2]any{{1672500000, "古い方"}, {1672600000, "新しい方"}}
	db := s.db
	for _, th := range threads {
		db.Exec(`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES ('livejupiter', ?, ?, 1)`,
			th[0], th[1])
	}
	status, body = get(t, s, "/api/search?q=%E3%81%82%E3%81%84&sort=old&from=2023-01&to=2023-02")
	if status != 200 {
		t.Fatalf("縮退の正常系 -> %d %v", status, body)
	}
	if fs.lastReq.SortOld {
		t.Error("縮退時に kako_old を引こうとしている")
	}
	items := body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["title"] != "古い方" {
		t.Errorf("反転されていない: %v", items)
	}
}

func TestExactModeDeepPage(t *testing.T) {
	fs := &fakeSearch{result: &index.Result{TotalFound: 30000}}
	cfg := config.Config{FiveChBase: "https://x", HasOld: true, OldMaxRangeYears: 2, ExactCount: true}
	s, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}
	// Exact モードでは 20 ページを超えられる（MaxResultWindow まで）
	status, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84&page=25&per=100")
	if status != 200 {
		t.Fatalf("exact モードの25ページ目 -> %d %v", status, body)
	}
	if body["max_page"].(float64) != 300 {
		t.Errorf("max_page = %v, want 300 (30000/100)", body["max_page"])
	}
	if q, _ := fs.lastReq.SQL(); !strings.Contains(q, "max_matches=2500") {
		t.Errorf("max_matches が offset+per に拡張されていない: %s", q)
	}
	// ウィンドウ上限（MaxResultWindow 件目）を1ページ超えたら 400。
	// 上限そのものは index.MaxResultWindow から導いて、値を変えてもテストが追随するようにする
	overPage := index.MaxResultWindow/100 + 1
	status, body = get(t, s, fmt.Sprintf("/api/search?q=%%E3%%81%%82%%E3%%81%%84&page=%d&per=100", overPage))
	if status != 400 || errCode(body) != "page_limit" {
		t.Errorf("ウィンドウ上限 -> %d %q, want 400 page_limit", status, errCode(body))
	}
	// 上限ちょうどのページは通る
	status, _ = get(t, s, fmt.Sprintf("/api/search?q=%%E3%%81%%82%%E3%%81%%84&page=%d&per=100", overPage-1))
	if status != 200 {
		t.Errorf("ウィンドウ上限ちょうどのページ -> %d, want 200", status)
	}
	// per の上限は 100
	status, _ = get(t, s, "/api/search?q=%E3%81%82%E3%81%84&per=100")
	if status != 200 {
		t.Errorf("per=100 -> %d, want 200", status)
	}
	status, body = get(t, s, "/api/search?q=%E3%81%82%E3%81%84&per=101")
	if status != 400 || errCode(body) != "invalid_per" {
		t.Errorf("per=101 -> %d %q, want 400 invalid_per", status, errCode(body))
	}
}

func TestBoardsEndpoint(t *testing.T) {
	s := newTestServer(t, &fakeSearch{})
	status, body := get(t, s, "/api/boards")
	if status != 200 {
		t.Fatalf("boards -> %d", status)
	}
	boards := body["boards"].([]any)
	if len(boards) != 2 {
		t.Fatalf("boards = %d", len(boards))
	}
	// thread_count 降順
	if boards[0].(map[string]any)["board_id"] != "livejupiter" {
		t.Errorf("並び順が thread_count 降順でない: %v", boards[0])
	}
	// D3 の追加フィールド（docs/api-contract-diff.md）
	if body["total_threads"].(float64) != 3 || body["total_boards"].(float64) != 2 {
		t.Errorf("統計フィールドが不正: %v %v", body["total_threads"], body["total_boards"])
	}
	if !strings.Contains(body["index_updated_at"].(string), "+09:00") {
		t.Errorf("index_updated_at が JST でない: %v", body["index_updated_at"])
	}
}

func TestVersionCheck(t *testing.T) {
	db := testDB(t)
	if err := checkNormalizerVersion(db, tokenizer.Version); err != nil {
		t.Errorf("一致しているのに拒否: %v", err)
	}
	if err := checkNormalizerVersion(db, tokenizer.Version+1); err == nil {
		t.Error("不一致なのに起動を許している（仕様書16.3違反）")
	}
	db.Exec(`DELETE FROM kako_meta`)
	if err := checkNormalizerVersion(db, tokenizer.Version); err == nil {
		t.Error("未記録なのに起動を許している")
	}
}

// 窓（MaxResultWindow）を超えるヒット数は結果を返さず 400 で跳ね返す（依頼者指示 2026-08-05）
func TestTooManyResults(t *testing.T) {
	cfg := config.Config{FiveChBase: "https://kako.5ch.io", HasOld: true,
		OldMaxRangeYears: 2, ExactCount: true}

	// 窓ちょうど → 通常どおり結果を返し、最後のページまで辿れる
	fs := &fakeSearch{result: &index.Result{TotalFound: index.MaxResultWindow}}
	s, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}
	status, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84&per=50")
	if status != 200 {
		t.Fatalf("窓ちょうど -> %d %v", status, body)
	}
	if want := float64(index.MaxResultWindow / 50); body["max_page"] != want {
		t.Errorf("max_page = %v, want %v（全件たどれる）", body["max_page"], want)
	}

	// 窓+1 → 400 too_many_results
	fs.result = &index.Result{TotalFound: index.MaxResultWindow + 1, Approximate: true}
	status, body = get(t, s, "/api/search?q=%E3%81%82%E3%81%84&per=50")
	if status != 400 || errCode(body) != "too_many_results" {
		t.Fatalf("窓超過 -> %d %q, want 400 too_many_results", status, errCode(body))
	}
	msg := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "25万") || !strings.Contains(msg, "絞り込") {
		t.Errorf("案内が不親切: %s", msg)
	}

	// 非 Exact モードでは従来どおり cutoff で頭打ちにするだけで、跳ね返さない
	cfg.ExactCount = false
	s2, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ = get(t, s2, "/api/search?q=%E3%81%82%E3%81%84&per=50"); status != 200 {
		t.Errorf("非Exact で跳ね返している: %d", status)
	}
}

// 検索語なしの一覧モード（依頼者指示 2026-08-06。仕様書1.2からの限定的な逸脱）。
// 板と期間の両方が指定され、かつヒットが窓（25万件）以下のときだけ開放する。
func TestBrowseMode(t *testing.T) {
	cfg := config.Config{FiveChBase: "https://kako.5ch.io", HasOld: true,
		OldMaxRangeYears: 2, ExactCount: true}
	fs := &fakeSearch{result: &index.Result{TotalFound: 1200}}
	s, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}

	// 板＋期間の両方あり → 開放
	status, body := get(t, s, "/api/search?board=livejupiter&from=2015-01&to=2015-12")
	if status != 200 {
		t.Fatalf("板＋期間の一覧 -> %d %v", status, body)
	}
	// @title を含まない板のみの式であること。検索語がないのに @title が付くと
	// 空フレーズになって構文エラーか全件不一致になる
	if fs.lastReq.Match != "@board (b_livejupiter)" {
		t.Errorf("MATCH = %q, want %q", fs.lastReq.Match, "@board (b_livejupiter)")
	}
	if !fs.lastReq.Exact {
		t.Error("一覧モードが Exact でない（25万件の判定が成立しない）")
	}
	// 期間が doc_id 範囲に落ちていること
	if fs.lastReq.TsFrom == 0 || fs.lastReq.TsTo == 0 {
		t.Errorf("期間が渡っていない: from=%d to=%d", fs.lastReq.TsFrom, fs.lastReq.TsTo)
	}
	if body["query"] != "" {
		t.Errorf("query = %v, want 空文字", body["query"])
	}

	// 日付単位（YYYY-MM-DD）の一覧（2026-08-06 追加）。年→月→日の絞り込み
	fs.result = &index.Result{TotalFound: 12}
	status, body = get(t, s, "/api/search?board=livejupiter&from=2017-03-05&to=2017-03-05")
	if status != 200 {
		t.Fatalf("日単位の一覧 -> %d %v", status, body)
	}
	dFrom, dTo, _ := docid.DayBounds("2017-03-05")
	if fs.lastReq.TsFrom != dFrom || fs.lastReq.TsTo != dTo {
		t.Errorf("日単位の期間 = (%d,%d), want (%d,%d)", fs.lastReq.TsFrom, fs.lastReq.TsTo, dFrom, dTo)
	}
	// 月と日を混ぜても通る（from は月初、to はその日の終わり）
	if status, _ := get(t, s, "/api/search?board=livejupiter&from=2017-03&to=2017-03-15"); status != 200 {
		t.Errorf("月と日の混在 -> %d", status)
	}
	// 存在しない日付は 400
	if status, body := get(t, s, "/api/search?q=%E3%81%82%E3%81%84&from=2017-02-30"); status != 400 || errCode(body) != "invalid_date" {
		t.Errorf("2017-02-30 -> %d %q, want 400 invalid_date", status, errCode(body))
	}

	// 条件が欠けている場合は従来どおり 400。板だけ・期間だけでは開放しない
	for _, u := range []string{
		"/api/search?board=livejupiter",              // 期間なし
		"/api/search?board=livejupiter&from=2015-01", // 期間の終わりがない
		"/api/search?board=livejupiter&to=2015-12",   // 期間の始まりがない
		"/api/search?from=2015-01&to=2015-12",        // 板なし
		"/api/search",
	} {
		if status, body := get(t, s, u); status != 400 || errCode(body) != "empty_query" {
			t.Errorf("%s -> %d %q, want 400 empty_query", u, status, errCode(body))
		}
	}

	// 正規化で全部消える検索語（U+FFFD のみ）は一覧に読み替えない。
	// 利用者は検索したつもりであり、無関係な一覧が出るのは取りこぼしより分かりにくい
	status, body = get(t, s, "/api/search?q=%EF%BF%BD&board=livejupiter&from=2015-01&to=2015-12")
	if status != 400 || errCode(body) != "empty_query" {
		t.Errorf("正規化で消える語 -> %d %q, want 400 empty_query", status, errCode(body))
	}

	// 窓を超えたら結果を返さず、一覧モード向けの案内にする
	fs.result = &index.Result{TotalFound: index.MaxResultWindow + 1, Approximate: true}
	status, body = get(t, s, "/api/search?board=livejupiter&from=2015-01&to=2015-12")
	if status != 400 || errCode(body) != "too_many_results" {
		t.Fatalf("窓超過の一覧 -> %d %q, want 400 too_many_results", status, errCode(body))
	}
	msg := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "期間を狭める") {
		t.Errorf("一覧モードの案内になっていない: %s", msg)
	}

	// 非 Exact モードでは件数が概算になり「25万件以下なら」を判定できないため開放しない
	cfg.ExactCount = false
	s2, err := newServer(cfg, testDB(t), fs)
	if err != nil {
		t.Fatal(err)
	}
	status, body = get(t, s2, "/api/search?board=livejupiter&from=2015-01&to=2015-12")
	if status != 400 || errCode(body) != "empty_query" {
		t.Errorf("非Exact で一覧を開放している -> %d %q", status, errCode(body))
	}
}

func TestHumanCount(t *testing.T) {
	for in, want := range map[int]string{250_000: "25万", 100_000: "10万", 1_000_000: "100万", 1234: "1234"} {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}
