// scrape: 過去ログ一覧ページから新規スレを取得して SQLite に投入する（夜間バッチ用）。
// 検索インデックスへの反映は次回の rebuild（indexer --rotate）で行われる。
// res_count の更新は SQLite のみで即時反映される（仕様書4.3・9.1）。
//
// 2026-08-21 追加: 5ch の過去ログ倉庫が止まっている板は 2ch.sc から補完する
// （docs/changes/2026-08-21.md）。sc 側の実装は scrapesc.go。
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"kakosearch/internal/docid"
	"kakosearch/internal/scrape"
	"kakosearch/internal/store"
)

type boardURL struct {
	BoardID string `json:"board_id"`
	URL     string `json:"url"`
}

func loadBoardURLs(path string) ([]boardURL, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var urls []boardURL
	if err := json.Unmarshal(raw, &urls); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return urls, nil
}

func cmdScrape(args []string) error {
	fs := flag.NewFlagSet("scrape", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	urlsPath := fs.String("urls", "./db/board-urls.json", "板→5ch過去ログサーバー対応表")
	scURLsPath := fs.String("sc-urls", "./db/board-urls-sc.json", "板→2ch.sc過去ログサーバー対応表（kakoctl scboards で生成）")
	boardFilter := fs.String("board", "", "対象を限定（カンマ区切りの board_id。テスト用）")
	delay := fs.Duration("delay", 500*time.Millisecond, "リクエスト間隔")
	maxPages := fs.Int("max-pages", 1000, "1板あたりの最大ページ数（安全弁）")
	dryRun := fs.Bool("dry-run", false, "取得と件数報告のみ行い、DB へ書き込まない")
	source := fs.String("source", "auto", "取得元: auto（5ch優先・停止板はscへ）/ 5ch（scを使わない）/ sc（scのみ）")
	staleHours := fs.Float64("sc-stale-hours", 48, "5chの過去ログ倉庫がこの時間以上更新されていない板を sc へ回す")
	maxDirs := fs.Int("sc-max-dirs", 20, "1板あたりに取得する sc 倉庫（subject.txt）の上限")
	fs.Parse(args)

	if *source != "auto" && *source != "5ch" && *source != "sc" {
		return fmt.Errorf("--source は auto / 5ch / sc のいずれか（指定: %s）", *source)
	}

	urls, err := loadBoardURLs(*urlsPath)
	if err != nil {
		return err
	}
	scURLs := map[string]string{}
	if *source != "5ch" {
		list, err := loadBoardURLs(*scURLsPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintf(os.Stderr,
				"警告: %s が無いため sc フォールバックは無効です（kakoctl scboards で生成してください）\n", *scURLsPath)
		}
		for _, b := range list {
			scURLs[b.BoardID] = b.URL
		}
	}

	var filter map[string]bool
	if *boardFilter != "" {
		filter = map[string]bool{}
		for _, b := range strings.Split(*boardFilter, ",") {
			filter[strings.TrimSpace(b)] = true
		}
	}

	db, err := store.Open(*dbPath, false)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.EnsureScrapeState(db); err != nil {
		return err
	}
	if err := store.EnsureSCState(db); err != nil {
		return err
	}
	state, err := store.LoadScrapeState(db)
	if err != nil {
		return err
	}

	anchorStmt, err := db.Prepare(
		`SELECT COALESCE(MAX(thread_key), 0) FROM threads WHERE board_id = ? AND thread_key < ?`)
	if err != nil {
		return err
	}
	defer anchorStmt.Close()
	existsStmt, err := db.Prepare(
		`SELECT res_count FROM threads WHERE board_id = ? AND thread_key = ?`)
	if err != nil {
		return err
	}
	defer existsStmt.Close()

	// ★★ sc から1行でも入れる前に、5ch 用アンカーを全板ぶん確定させる。
	// これを飛ばすと、sc 由来の新しい thread_key が threads の MAX を押し上げ、
	// 5ch の倉庫が復旧しても停止期間ぶんを取りに行かなくなる（docs/changes/2026-08-21.md）。
	nBoot, err := bootstrapAnchors(db, anchorStmt, urls, filter, state, *dryRun)
	if err != nil {
		return err
	}
	if nBoot > 0 {
		fmt.Printf("scrape: 5ch用アンカーを%d板ぶん初期化しました\n", nBoot)
	}

	client := scrape.New(*delay)
	start := time.Now()
	var totalNew, totalUpd, scNew, scUpd int64
	var nSkip404, nNoChange, nStale, nErr, nDone, nScanned int
	var nSCBoards, nSCDirs, nSCSkip int
	var newestLatest time.Time

	for _, bu := range urls {
		if filter != nil && !filter[bu.BoardID] {
			continue
		}
		st := state[bu.BoardID]

		var r boardResult
		var scanErr error
		if *source == "sc" {
			r = boardResult{status: "stale", latest: st.LatestAt}
		} else {
			r, scanErr = scrapeBoard(db, client, anchorStmt, existsStmt, bu, *maxPages, *dryRun, st)
			if scanErr != nil {
				nErr++
				fmt.Fprintf(os.Stderr, "[%s] エラー: %v\n", bu.BoardID, scanErr)
			} else {
				nDone++
				switch r.status {
				case "404":
					nSkip404++
				case "stale":
					nStale++
				case "nochange":
					nNoChange++
					nScanned++
				default:
					nScanned++
				}
				if r.latest.After(newestLatest) {
					newestLatest = r.latest
				}
				totalNew += r.nNew
				totalUpd += r.nUpd
			}
		}

		// --- sc フォールバックの判定 ---
		// 5ch の倉庫が止まっている（Latest update が古い）、404、または取得エラーの板を回す。
		if *source == "5ch" {
			continue
		}
		base, ok := scURLs[bu.BoardID]
		if !ok {
			continue // sc に無い板（新設板など。全907板中74板）
		}
		latest := r.latest
		if latest.IsZero() {
			latest = st.LatestAt
		}
		stalled := scanErr != nil || r.status == "404" ||
			(!latest.IsZero() && time.Since(latest) > time.Duration(*staleHours*float64(time.Hour)))
		if *source != "sc" && !stalled {
			continue
		}

		anchor := state[bu.BoardID].Anchor5ch
		sr, err := scrapeBoardSC(db, client, existsStmt, bu.BoardID, base, anchor, *maxDirs, *dryRun)
		if err != nil {
			nErr++
			fmt.Fprintf(os.Stderr, "[%s] sc エラー: %v\n", bu.BoardID, err)
			continue
		}
		nSCBoards++
		nSCDirs += sr.dirsFetched
		nSCSkip += sr.dirsSkipped
		scNew += sr.nNew
		scUpd += sr.nUpd
	}

	mode := ""
	if *dryRun {
		mode = "（dry-run: 書き込みなし）"
	}
	fmt.Printf("scrape: %d板処理 新規%d件 res_count更新%d件 / 404スキップ%d板 未更新スキップ%d板 走査%d板（うち更新なし%d板）エラー%d板 (%s)%s\n",
		nDone, totalNew, totalUpd, nSkip404, nStale, nScanned, nNoChange, nErr,
		time.Since(start).Round(time.Second), mode)
	if nSCBoards > 0 || *source == "sc" {
		fmt.Printf("scrape[sc]: %d板を2ch.scから補完 新規%d件 res_count更新%d件 / subject.txt取得%d件 変化なしスキップ%d件\n",
			nSCBoards, scNew, scUpd, nSCDirs, nSCSkip)
	}
	if !newestLatest.IsZero() {
		age := time.Since(newestLatest)
		fmt.Printf("scrape: 取得元の最終更新 %s（%.1f時間前）\n",
			newestLatest.Format("2006-01-02 15:04:05"), age.Hours())
		// 全板の Latest update が揃って古いときは、こちらの不具合ではなく取得元
		// （5ch の過去ログ倉庫）の生成が止まっている。2026-08-07〜 に実際に発生した。
		if age > 36*time.Hour {
			fmt.Fprintf(os.Stderr,
				"警告: 5ch の過去ログが %.1f 時間更新されていません（%d板を sc から補完）\n",
				age.Hours(), nSCBoards)
		}
	}

	// フロントの「補完中」表示に使う（sc から補完した板が0になれば自動的に消える）。
	if !*dryRun {
		now := time.Now()
		if err := store.SetMeta(db, "scrape_last_run", now.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if err := store.SetMeta(db, "sc_fallback_boards", fmt.Sprint(nSCBoards)); err != nil {
			return err
		}
		if !newestLatest.IsZero() {
			if err := store.SetMeta(db, "fivech_latest_at", newestLatest.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
	}

	if nErr > 0 && nDone == 0 && nSCBoards == 0 {
		return fmt.Errorf("全板の取得に失敗（ネットワーク断の可能性）")
	}
	// 一部の板のエラーは終了コードに反映しない: 夜間バッチで後続の再構築を止めないため。
	// 該当板は stderr に残り、次回実行時にアンカーが進んでいないため自動で追い付く。
	return nil
}

// bootstrapAnchors は anchor_5ch が未記録の板について、現在の threads の最新 thread_key を
// 5ch 用アンカーとして確定させる。sc から1行でも取り込む前に必ず通ること。
func bootstrapAnchors(db *sql.DB, anchorStmt *sql.Stmt, urls []boardURL, filter map[string]bool,
	state map[string]store.BoardState, dryRun bool) (int, error) {

	now := time.Now()
	n := 0
	for _, bu := range urls {
		if filter != nil && !filter[bu.BoardID] {
			continue
		}
		if state[bu.BoardID].Anchor5ch > 0 {
			continue
		}
		var anchor int64
		if err := anchorStmt.QueryRow(bu.BoardID, docid.ThreadKeyExcludeMin).Scan(&anchor); err != nil {
			return n, err
		}
		if anchor == 0 {
			continue // その板の行がまだ1件も無い（新規板）。アンカー無しで全件取得する
		}
		if !dryRun {
			if err := store.SaveScrapeState(db, bu.BoardID, time.Time{}, anchor, now); err != nil {
				return n, err
			}
		}
		st := state[bu.BoardID]
		st.Anchor5ch = anchor
		state[bu.BoardID] = st
		n++
	}
	return n, nil
}

// boardResult は1板分の処理結果。
type boardResult struct {
	nNew, nUpd int64
	status     string    // "ok" / "404" / "stale"（未更新スキップ）/ "nochange"
	latest     time.Time // 取得元ページが申告する Latest update
}

// scrapeBoard は1板分を 5ch から処理する。
// st.LatestAt は前回この板を取り込み切ったときの Latest update（ゼロ値なら門番は働かない）。
// st.Anchor5ch は 5ch の一覧で最後に見た最新 thread_key。
func scrapeBoard(db *sql.DB, client *scrape.Client, anchorStmt, existsStmt *sql.Stmt,
	bu boardURL, maxPages int, dryRun bool, st store.BoardState) (boardResult, error) {

	// ★ アンカーは threads の MAX ではなく scrape_state の記録を使う。
	// sc から補完した行で MAX が先へ飛んでも、5ch の走査範囲を巻き戻さないため。
	anchor := st.Anchor5ch
	if anchor == 0 {
		// 未記録の板（bootstrap で anchor が 0 だった＝行が無い板）。従来どおり DB から取る
		if err := anchorStmt.QueryRow(bu.BoardID, docid.ThreadKeyExcludeMin).Scan(&anchor); err != nil {
			return boardResult{}, err
		}
	}

	known := func(tk int64) bool {
		var rc int64
		return existsStmt.QueryRow(bu.BoardID, tk).Scan(&rc) == nil
	}
	res, err := client.ScanBoard(bu.URL, anchor, known, maxPages, st.LatestAt)
	if err != nil {
		return boardResult{}, err
	}
	if res.Skipped {
		// 取得元のページが前回から進んでいない。scrape_state は据え置いてよい
		// （同じ値を書くだけなので意味が無い）。
		return boardResult{status: "stale", latest: res.Latest}, nil
	}
	if res.Pages == 0 {
		return boardResult{status: "404", latest: res.Latest}, nil
	}

	// sc から補完済みの行は、5ch 側で取り直せた時点で title ごと 5ch の値へ寄せる
	// （仕様書9.3 への意図的な例外。docs/changes/2026-08-21.md の設計判断4）。
	scKeys, err := store.SCSourcedKeys(db, bu.BoardID)
	if err != nil {
		return boardResult{}, err
	}

	// 既存判定と差分計算はトランザクションの外で済ませ、書き込みは1トランザクションに
	// まとめる。途中で失敗した場合は何も書かれず、アンカーが進まないため次回に再試行される。
	type upd struct {
		tk, rc int64
		title  string // 空でなければ title も上書きする（sc 由来行の是正）
		fromSC bool
	}
	var inserts []scrape.Entry
	var updates []upd
	var newAnchor int64
	nowUnix := time.Now().Unix()
	for _, e := range res.Entries {
		if e.ThreadKey > newAnchor && e.ThreadKey < nowUnix {
			newAnchor = e.ThreadKey
		}
		var rc int64
		serr := existsStmt.QueryRow(bu.BoardID, e.ThreadKey).Scan(&rc)
		switch {
		case serr == sql.ErrNoRows:
			inserts = append(inserts, e)
		case serr != nil:
			return boardResult{}, serr
		case scKeys[e.ThreadKey]:
			updates = append(updates, upd{e.ThreadKey, e.ResCount, e.Title, true})
		case rc != e.ResCount:
			// 5ch 由来の既存行は res_count のみ更新。title は書き換えない（仕様書9.3）
			updates = append(updates, upd{tk: e.ThreadKey, rc: e.ResCount})
		}
	}
	if newAnchor < anchor {
		newAnchor = anchor // 一覧が巻き戻ることは無いはずだが、アンカーは後退させない
	}

	// 取得が不完全だった板は Latest update を記録しない。記録すると、取得元のページが
	// 次に更新されるまでその板が丸ごとスキップされ、取りこぼしたぶんが埋まらなくなる。
	complete := !res.HitLimit
	if res.HitLimit {
		fmt.Fprintf(os.Stderr, "[%s] 警告: max-pages=%d に到達。取得は不完全の可能性（Latest update は記録せず次回再試行）\n",
			bu.BoardID, maxPages)
	} else if anchor > 0 && !res.AnchorFound && !res.StoppedOld {
		fmt.Fprintf(os.Stderr, "[%s] 警告: アンカー%d に到達しないまま末尾。全%d件を投入\n",
			bu.BoardID, anchor, len(inserts))
	}

	out := boardResult{
		nNew:   int64(len(inserts)),
		nUpd:   int64(len(updates)),
		status: "ok",
		latest: res.Latest,
	}
	if len(inserts) == 0 && len(updates) == 0 {
		out.status = "nochange"
	}

	if dryRun {
		if out.status == "nochange" {
			return out, nil
		}
		fmt.Printf("[%s] 新規%d件 更新%d件 (%dページ)\n", bu.BoardID, len(inserts), len(updates), res.Pages)
		return out, nil
	}

	// 新規・更新が無い板でも Latest update とアンカーだけは記録する。ページは更新されたが
	// 中身は全て既知だった、というケースを次回スキップできるようにするため。
	if out.status == "nochange" {
		if complete {
			if err := store.SaveScrapeState(db, bu.BoardID, res.Latest, newAnchor, time.Now()); err != nil {
				return boardResult{}, err
			}
		}
		return out, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return boardResult{}, err
	}
	defer tx.Rollback()
	insStmt, err := tx.Prepare(
		`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return boardResult{}, err
	}
	updStmt, err := tx.Prepare(
		`UPDATE threads SET res_count = ? WHERE board_id = ? AND thread_key = ?`)
	if err != nil {
		return boardResult{}, err
	}
	updBothStmt, err := tx.Prepare(
		`UPDATE threads SET res_count = ?, title = ? WHERE board_id = ? AND thread_key = ?`)
	if err != nil {
		return boardResult{}, err
	}
	for _, e := range inserts {
		if _, err := insStmt.Exec(bu.BoardID, e.ThreadKey, e.Title, e.ResCount); err != nil {
			return boardResult{}, err
		}
	}
	nFixed := 0
	for _, u := range updates {
		if u.fromSC {
			if _, err := updBothStmt.Exec(u.rc, u.title, bu.BoardID, u.tk); err != nil {
				return boardResult{}, err
			}
			if err := store.ClearSCSourced(tx, bu.BoardID, u.tk); err != nil {
				return boardResult{}, err
			}
			nFixed++
			continue
		}
		if _, err := updStmt.Exec(u.rc, bu.BoardID, u.tk); err != nil {
			return boardResult{}, err
		}
	}
	// ★ Latest update とアンカーの記録は投入と同一トランザクションに入れる。別々にすると
	// 「状態だけ進んで中身が入っていない」板が生まれ、その差分が永久に埋まらない。
	if complete {
		if err := store.SaveScrapeState(tx, bu.BoardID, res.Latest, newAnchor, time.Now()); err != nil {
			return boardResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return boardResult{}, err
	}

	if nFixed > 0 {
		fmt.Printf("[%s] 新規%d件 更新%d件（うちsc由来の是正%d件） (%dページ)\n",
			bu.BoardID, len(inserts), len(updates), nFixed, res.Pages)
	} else {
		fmt.Printf("[%s] 新規%d件 更新%d件 (%dページ)\n", bu.BoardID, len(inserts), len(updates), res.Pages)
	}
	return out, nil
}
