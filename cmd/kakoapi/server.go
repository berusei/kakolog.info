package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"kakosearch/internal/config"
	"kakosearch/internal/docid"
	"kakosearch/internal/index"
	"kakosearch/internal/store"
)

// Searcher は Manticore クライアントの差し替え点（テスト用）。
type Searcher interface {
	Search(ctx context.Context, req index.Request) (*index.Result, error)
	Ping(ctx context.Context) error
}

type Server struct {
	cfg    config.Config
	db     *sql.DB
	search Searcher

	// boardHost: board_id → "https://<host>"（板ごとの過去ログサーバー。仕様書16.1）
	boardHost map[string]string

	mu           sync.RWMutex
	boardsByIdx  map[int64]store.Board
	boardsByID   map[string]store.Board
	boardsJSON   []byte
	exclusions   map[string]bool
	indexBuiltAt string // RFC3339(JST)。索引をビルドした時刻
	// dataUpdatedAt は「DB に入っている最新スレの作成時刻」。フッターに出すのはこちら。
	// 取得元が止まっていても再構築は毎回走るため、ビルド時刻はデータの新しさを表さない
	// （docs/changes/2026-08-09.md の未修正バグ。2026-08-21 修正）。
	dataUpdatedAt string // RFC3339(JST)
	// scFallback は直近のスクレイプで 2ch.sc から補完した板があったことを示す。
	// 5ch の過去ログ倉庫が復旧すれば次回の実行で 0 に戻り、表示も自動的に消える。
	scFallback bool
}

func newServer(cfg config.Config, db *sql.DB, search Searcher) (*Server, error) {
	s := &Server{cfg: cfg, db: db, search: search}
	s.boardHost = loadBoardHosts(cfg.BoardURLs)
	if err := s.reloadBoards(); err != nil {
		return nil, fmt.Errorf("boards の読み込みに失敗: %w", err)
	}
	if err := s.reloadExclusions(); err != nil {
		return nil, fmt.Errorf("除外リストの読み込みに失敗: %w", err)
	}
	return s, nil
}

func (s *Server) reloadBoards() error {
	boards, err := store.AllBoards(s.db)
	if err != nil {
		return err
	}
	byIdx := make(map[int64]store.Board, len(boards))
	byID := make(map[string]store.Board, len(boards))
	var totalThreads int64
	for _, b := range boards {
		byIdx[b.BoardIdx] = b
		byID[b.BoardID] = b
		totalThreads += b.ThreadCount
	}
	builtAt, err := store.Meta(s.db, "index_built_at")
	if err != nil {
		return err
	}
	if t, perr := time.Parse(time.RFC3339, builtAt); perr == nil {
		builtAt = t.In(docid.JST).Format(time.RFC3339)
	}
	dataAt, err := store.Meta(s.db, "data_updated_at")
	if err != nil {
		return err
	}
	if t, perr := time.Parse(time.RFC3339, dataAt); perr == nil {
		dataAt = t.In(docid.JST).Format(time.RFC3339)
	} else {
		// 旧世代の DB（data_updated_at 未記録）ではビルド時刻で代替する。
		// 次回の kakoctl stamp で正しい値に入れ替わる
		dataAt = builtAt
	}
	scBoards, err := store.Meta(s.db, "sc_fallback_boards")
	if err != nil {
		return err
	}
	scFallback := scBoards != "" && scBoards != "0"
	// total_threads / total_boards / index_updated_at は契約差分 D3 の裁定による追加
	// （docs/api-contract-diff.md、依頼者承認 2026-08-05）。
	// data_updated_at / sc_fallback は 2026-08-21 追加（docs/changes/2026-08-21.md）
	j, err := json.Marshal(map[string]any{
		"boards":           boards,
		"total_threads":    totalThreads,
		"total_boards":     len(boards),
		"index_updated_at": builtAt,
		"data_updated_at":  dataAt,
		"sc_fallback":      scFallback,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.boardsByIdx, s.boardsByID, s.boardsJSON, s.indexBuiltAt = byIdx, byID, j, builtAt
	s.dataUpdatedAt, s.scFallback = dataAt, scFallback
	s.mu.Unlock()
	return nil
}

func (s *Server) reloadExclusions() error {
	ex, err := store.LoadExclusions(s.db)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.exclusions = ex
	s.mu.Unlock()
	return nil
}

// refreshLoop は除外リスト（60秒。仕様書16.4の即時反映経路）と
// 板・メタ情報（10分）を定期再読込する。
func (s *Server) refreshLoop(ctx context.Context) {
	exTick := time.NewTicker(60 * time.Second)
	bTick := time.NewTicker(10 * time.Minute)
	defer exTick.Stop()
	defer bTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-exTick.C:
			if err := s.reloadExclusions(); err != nil {
				log.Printf("除外リスト再読込に失敗: %v", err)
			}
		case <-bTick.C:
			if err := s.reloadBoards(); err != nil {
				log.Printf("boards 再読込に失敗: %v", err)
			}
		}
	}
}

// loadBoardHosts は板ごとの過去ログサーバー対応表（[{"board_id","url"}]）を読み、
// board_id → "https://<host>" の対応を返す。ファイルが無ければ空（全て FiveChBase に
// フォールバック）。ドメイン再移転時は JSON 差し替え + 再起動のみで復旧（仕様書16.1）。
func loadBoardHosts(path string) map[string]string {
	m := map[string]string{}
	if path == "" {
		return m
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("板URL対応表を読めないため既定ドメインを使用: %v", err)
		return m
	}
	var list []struct {
		BoardID string `json:"board_id"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Printf("板URL対応表のパースに失敗、既定ドメインを使用: %v", err)
		return m
	}
	for _, e := range list {
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		m[e.BoardID] = u.Scheme + "://" + u.Host
	}
	log.Printf("板URL対応表: %d板を読み込み（%s）", len(m), path)
	return m
}

// --- エラーレスポンス（仕様書10.4） ---

func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// humanCount は件数を日本語の読みやすい単位にする（25万件の案内文用）。
func humanCount(n int) string {
	switch {
	case n >= 10000 && n%10000 == 0:
		return fmt.Sprintf("%d万", n/10000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// --- /api/search（仕様書10.1） ---

type searchItem struct {
	BoardID   string `json:"board_id"`
	BoardName string `json:"board_name"`
	ThreadKey string `json:"thread_key"`
	Title     string `json:"title"`
	ResCount  int64  `json:"res_count"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"url"`
}

type searchResponse struct {
	Query              string       `json:"query"`
	Total              int          `json:"total"`
	TotalIsApproximate bool         `json:"total_is_approximate"`
	Page               int          `json:"page"`
	PerPage            int          `json:"per_page"`
	MaxPage            int          `json:"max_page"`
	IndexUpdatedAt     string       `json:"index_updated_at"`
	DataUpdatedAt      string       `json:"data_updated_at"`
	SCFallback         bool         `json:"sc_fallback"`
	TookMs             int64        `json:"took_ms"`
	Items              []searchItem `json:"items"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	qv := r.URL.Query()

	// q が空でも即座には弾かない。板と期間の両方が指定されていれば一覧モードを
	// 開放するため、判定は board / from / to を解釈したあと（browse）で行う。
	q := strings.TrimSpace(qv.Get("q"))

	per := index.PerPage
	if v := qv.Get("per"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > index.MaxPer {
			apiError(w, 400, "invalid_per",
				fmt.Sprintf("表示件数は1〜%dで指定してください", index.MaxPer))
			return
		}
		per = n
	}

	// ページ上限: Exact モードでは結果位置 MaxResultWindow まで、
	// 非 Exact では仕様書12.1どおり cutoff=1000 の範囲まで
	window := index.MaxMatches
	if s.cfg.ExactCount {
		window = index.MaxResultWindow
	}
	maxAllowedPage := window / per

	page := 1
	if v := qv.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			apiError(w, 400, "invalid_page", "ページ番号が不正です")
			return
		}
		if n > maxAllowedPage {
			// 深いページネーションの可用性ガード（仕様書12.1。Exact モードでは拡張値）
			apiError(w, 400, "page_limit", fmt.Sprintf(
				"表示できるのは%dページまでです。検索条件を絞り込んでください", maxAllowedPage))
			return
		}
		page = n
	}

	sortOld := false
	switch qv.Get("sort") {
	case "", "new":
	case "old":
		sortOld = true
	default:
		apiError(w, 400, "invalid_sort", "sort は new または old を指定してください")
		return
	}

	var boards []string
	if v := qv.Get("board"); v != "" {
		s.mu.RLock()
		for _, b := range strings.Split(v, ",") {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			if _, ok := s.boardsByID[b]; !ok {
				s.mu.RUnlock()
				apiError(w, 400, "unknown_board", "指定された板が存在しません: "+b)
				return
			}
			boards = append(boards, b)
		}
		s.mu.RUnlock()
	}

	var tsFrom, tsTo int64
	if v := qv.Get("from"); v != "" {
		f, _, err := docid.PeriodBounds(v)
		if err != nil {
			apiError(w, 400, "invalid_date", err.Error())
			return
		}
		tsFrom = f
	}
	if v := qv.Get("to"); v != "" {
		_, t, err := docid.PeriodBounds(v)
		if err != nil {
			apiError(w, 400, "invalid_date", err.Error())
			return
		}
		tsTo = t
	}
	if tsFrom != 0 && tsTo != 0 && tsFrom > tsTo {
		apiError(w, 400, "invalid_date", "期間の開始が終了より後になっています")
		return
	}

	// 検索語なしの一覧モード（依頼者指示 2026-08-06。仕様書1.2からの逸脱の詳細は
	// index.BuildBrowseMatch のコメント）。開放の条件は3つとも必須:
	//   1. 板が指定されている（走査範囲を1板に閉じる）
	//   2. 期間の開始と終了の両方が指定されている（id BETWEEN で走査範囲を年に閉じる）
	//   3. Exact モードである（25万件の窓の判定が成立するのはこのモードだけ。
	//      非 Exact では cutoff=1000 で件数が概算になり「25万件以下なら」が判定できない）
	// 窓を超えるヒットは、下の too_many_results で結果を返さずに跳ね返す。
	browse := q == "" && len(boards) > 0 && tsFrom != 0 && tsTo != 0 && s.cfg.ExactCount

	var match string
	if browse {
		match, _ = index.BuildBrowseMatch(boards)
	} else {
		if q == "" {
			msg := "検索語を入力してください"
			if s.cfg.ExactCount {
				msg += "。板と期間の両方を指定した場合は、検索語なしで一覧できます"
			}
			apiError(w, 400, "empty_query", msg)
			return
		}
		words := index.ParseQuery(q)
		var ok bool
		match, ok = index.BuildMatch(words, boards)
		if !ok {
			// 正規化の結果すべての語が消えた場合（記号のみ等）。
			// 板・期間が揃っていても一覧に読み替えない。利用者は検索したつもりであり、
			// 無関係な一覧が出るのは取りこぼしより分かりにくい
			apiError(w, 400, "empty_query", "検索できる文字が含まれていません")
			return
		}

		// 1文字クエリのガード（仕様書12.2 MUST）
		if index.PositiveTokenCount(words) == 1 && len(boards) == 0 && tsFrom == 0 && tsTo == 0 {
			apiError(w, 400, "need_filter", "1文字での検索は板または期間の指定が必要です")
			return
		}
	}

	// kako_old 不在時の縮退運用（仕様書12.3）
	fallbackOld := sortOld && !s.cfg.HasOld
	if fallbackOld {
		// 縮退時は反転用に先頭 MaxMatches 件しか取れないため、それを超えるページは不可
		if page*per > index.MaxMatches {
			apiError(w, 400, "page_limit", fmt.Sprintf(
				"古い順の検索では%dページまでです。期間を絞り込んでください", index.MaxMatches/per))
			return
		}
		if tsFrom == 0 || tsTo == 0 {
			apiError(w, 400, "need_range",
				"古い順の検索には期間（from と to の両方）の指定が必要です")
			return
		}
		maxSpan := int64(s.cfg.OldMaxRangeYears) * 366 * 86400
		if tsTo-tsFrom > maxSpan {
			apiError(w, 400, "range_too_wide",
				fmt.Sprintf("古い順の検索は期間を%d年以内に絞ってください", s.cfg.OldMaxRangeYears))
			return
		}
	}

	req := index.Request{
		Match:      match,
		SortOld:    sortOld && s.cfg.HasOld,
		TsFrom:     tsFrom,
		TsTo:       tsTo,
		Page:       page,
		Per:        per,
		Exact:      s.cfg.ExactCount,
		ExactLimit: s.cfg.ExactCountLimit,
	}
	if fallbackOld {
		// kako_new を期間で絞って引き、全件（cutoff まで）を取って反転する。
		// 期間内のヒットが cutoff を超える場合は新しい側の1000件に限られる
		// （期間幅の上限があるため実用上は稀。仕様書12.3の合意済み縮退）
		req.Page = 1
		req.Per = index.MaxMatches
	}

	res, err := s.search.Search(r.Context(), req)
	if err != nil {
		log.Printf("search error: %v", err)
		apiError(w, 503, "search_unavailable",
			"検索エンジンが応答していません。しばらくしてからお試しください")
		return
	}

	// ヒット数が窓を超えたら結果を返さず、絞り込みを促す（依頼者指示 2026-08-05）。
	// cutoff を窓+1 に置いているため、ここに来る時点で「窓を1件でも超えたか」だけが
	// 分かっており、正確な総数は数えていない（数え切ると最悪ケースで数秒かかる）。
	if s.cfg.ExactCount && res.TotalFound > index.MaxResultWindow {
		if browse {
			// 一覧モードで窓を超えた場合。板と期間はすでに指定済みなので
			// 「板・期間を指定して」と案内しても打つ手がない
			apiError(w, 400, "too_many_results", fmt.Sprintf(
				"この板・期間のスレッドは多すぎて一覧できません（%s件以上）。期間を狭めるか、検索語を入れて絞り込んでください",
				humanCount(index.MaxResultWindow)))
			return
		}
		apiError(w, 400, "too_many_results", fmt.Sprintf(
			"該当が多すぎます（%s件以上）。板・期間を指定するか、語を増やして絞り込んでください",
			humanCount(index.MaxResultWindow)))
		return
	}

	ids := res.IDs
	if fallbackOld {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
		lo := (page - 1) * per
		if lo > len(ids) {
			lo = len(ids)
		}
		hi := lo + per
		if hi > len(ids) {
			hi = len(ids)
		}
		ids = ids[lo:hi]
	}

	s.mu.RLock()
	byIdx := s.boardsByIdx
	exclusions := s.exclusions
	builtAt := s.indexBuiltAt
	dataAt := s.dataUpdatedAt
	scFallback := s.scFallback
	s.mu.RUnlock()

	items := make([]searchItem, 0, len(ids))
	for _, id := range ids {
		tk, bidx := req.Decompose(id)
		board, ok := byIdx[bidx]
		if !ok {
			log.Printf("doc_id %d: board_idx %d が boards に存在しない", id, bidx)
			continue
		}
		if exclusions[fmt.Sprintf("%s/%d", board.BoardID, tk)] {
			continue // 削除依頼による除外（仕様書16.4）
		}
		th, err := store.LookupThread(r.Context(), s.db, board.BoardID, tk)
		if err != nil {
			log.Printf("SQLite lookup error: %v", err)
			apiError(w, 503, "store_unavailable", "データベースが応答していません")
			return
		}
		if th == nil {
			continue // インデックスと SQLite の不整合（再構築までの過渡状態）
		}
		base := s.boardHost[board.BoardID]
		if base == "" {
			base = s.cfg.FiveChBase
		}
		items = append(items, searchItem{
			BoardID:   board.BoardID,
			BoardName: board.BoardName,
			ThreadKey: strconv.FormatInt(tk, 10),
			Title:     th.Title,
			ResCount:  th.ResCount,
			CreatedAt: docid.CreatedAt(tk).Format(time.RFC3339),
			URL:       fmt.Sprintf("%s/test/read.cgi/%s/%d/", base, board.BoardID, tk),
		})
	}

	maxPage := (res.TotalFound + per - 1) / per
	if maxPage > maxAllowedPage {
		maxPage = maxAllowedPage
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	json.NewEncoder(w).Encode(searchResponse{
		Query:              q,
		Total:              res.TotalFound,
		TotalIsApproximate: res.Approximate,
		Page:               page,
		PerPage:            per,
		MaxPage:            maxPage,
		IndexUpdatedAt:     builtAt,
		DataUpdatedAt:      dataAt,
		SCFallback:         scFallback,
		TookMs:             time.Since(start).Milliseconds(),
		Items:              items,
	})
}

// --- /api/boards（仕様書10.2） ---

func (s *Server) handleBoards(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	j := s.boardsJSON
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(j)
}

// --- /api/health（仕様書10.3） ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		apiError(w, 503, "store_unavailable", "データベースが応答していません")
		return
	}
	if err := s.search.Ping(ctx); err != nil {
		apiError(w, 503, "search_unavailable", "検索エンジンが応答していません")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/boards", s.handleBoards)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	if s.cfg.StaticDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(s.cfg.StaticDir)))
	}
	return logMiddleware(mux)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}

var errVersionMismatch = errors.New("normalizer version mismatch")

// checkNormalizerVersion は起動時照合（仕様書16.3 MUST）。
// 不一致のまま起動すると静かに誤った検索結果を返すため、起動を拒否する。
func checkNormalizerVersion(db *sql.DB, codeVersion int) error {
	v, err := store.Meta(db, "normalizer_version")
	if err != nil {
		return err
	}
	if v == "" {
		return fmt.Errorf("%w: kako_meta に normalizer_version が未記録です。"+
			"インデックス構築後に kakoctl stamp を実行してください", errVersionMismatch)
	}
	if v != strconv.Itoa(codeVersion) {
		return fmt.Errorf("%w: インデックス=%s, コード=%d。"+
			"正規化ロジックが変わっています。インデックスを再構築してください",
			errVersionMismatch, v, codeVersion)
	}
	return nil
}
