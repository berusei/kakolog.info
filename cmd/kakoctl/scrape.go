// scrape: 過去ログ一覧ページから新規スレを取得して SQLite に投入する（日次バッチ用）。
// 検索インデックスへの反映は次回の rebuild（indexer --rotate）で行われる。
// res_count の更新は SQLite のみで即時反映される（仕様書4.3・9.1）。
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"kakosearch/internal/scrape"
	"kakosearch/internal/store"
)

type boardURL struct {
	BoardID string `json:"board_id"`
	URL     string `json:"url"`
}

func cmdScrape(args []string) error {
	fs := flag.NewFlagSet("scrape", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	urlsPath := fs.String("urls", "./db/board-urls.json", "板→過去ログサーバー対応表")
	boardFilter := fs.String("board", "", "対象を限定（カンマ区切りの board_id。テスト用）")
	delay := fs.Duration("delay", 500*time.Millisecond, "リクエスト間隔")
	maxPages := fs.Int("max-pages", 1000, "1板あたりの最大ページ数（安全弁）")
	dryRun := fs.Bool("dry-run", false, "取得と件数報告のみ行い、DB へ書き込まない")
	fs.Parse(args)

	raw, err := os.ReadFile(*urlsPath)
	if err != nil {
		return err
	}
	var urls []boardURL
	if err := json.Unmarshal(raw, &urls); err != nil {
		return fmt.Errorf("%s: %w", *urlsPath, err)
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

	client := scrape.New(*delay)
	start := time.Now()
	var totalNew, totalUpd int64
	var nSkip404, nNoChange, nErr, nDone int

	for _, bu := range urls {
		if filter != nil && !filter[bu.BoardID] {
			continue
		}
		nNew, nUpd, status, err := scrapeBoard(db, client, anchorStmt, existsStmt, bu, *maxPages, *dryRun)
		if err != nil {
			nErr++
			fmt.Fprintf(os.Stderr, "[%s] エラー: %v\n", bu.BoardID, err)
			continue
		}
		nDone++
		switch status {
		case "404":
			nSkip404++
		case "nochange":
			nNoChange++
		}
		totalNew += nNew
		totalUpd += nUpd
	}

	mode := ""
	if *dryRun {
		mode = "（dry-run: 書き込みなし）"
	}
	fmt.Printf("scrape: %d板処理 新規%d件 res_count更新%d件 / 404スキップ%d板 更新なし%d板 エラー%d板 (%s)%s\n",
		nDone, totalNew, totalUpd, nSkip404, nNoChange, nErr, time.Since(start).Round(time.Second), mode)
	if nErr > 0 && nDone == 0 {
		return fmt.Errorf("全板の取得に失敗（ネットワーク断の可能性）")
	}
	// 一部の板のエラーは終了コードに反映しない: 夜間バッチで後続の再構築を止めないため。
	// 該当板は stderr に残り、次回実行時にアンカーが進んでいないため自動で追い付く。
	return nil
}

// scrapeBoard は1板分を処理する。status は "ok" / "404" / "nochange"。
func scrapeBoard(db *sql.DB, client *scrape.Client, anchorStmt, existsStmt *sql.Stmt,
	bu boardURL, maxPages int, dryRun bool) (nNew, nUpd int64, status string, err error) {

	// アンカー = DB上のその板の最新スレ。日付バグスレ（未来の thread_key）は除外する
	now := time.Now().Unix()
	var anchor int64
	if err := anchorStmt.QueryRow(bu.BoardID, now).Scan(&anchor); err != nil {
		return 0, 0, "", err
	}

	known := func(tk int64) bool {
		var rc int64
		return existsStmt.QueryRow(bu.BoardID, tk).Scan(&rc) == nil
	}
	res, err := client.ScanBoard(bu.URL, anchor, known, maxPages)
	if err != nil {
		return 0, 0, "", err
	}
	if res.Pages == 0 {
		return 0, 0, "404", nil
	}

	// 既存判定と差分計算はトランザクションの外で済ませ、書き込みは1トランザクションに
	// まとめる。途中で失敗した場合は何も書かれず、アンカーが進まないため次回に再試行される。
	type upd struct{ tk, rc int64 }
	var inserts []scrape.Entry
	var updates []upd
	for _, e := range res.Entries {
		var rc int64
		serr := existsStmt.QueryRow(bu.BoardID, e.ThreadKey).Scan(&rc)
		switch {
		case serr == sql.ErrNoRows:
			inserts = append(inserts, e)
		case serr != nil:
			return 0, 0, "", serr
		case rc != e.ResCount:
			// 既存行は res_count のみ更新。title は書き換えない（仕様書9.3）
			updates = append(updates, upd{e.ThreadKey, e.ResCount})
		}
	}

	if res.HitLimit {
		fmt.Fprintf(os.Stderr, "[%s] 警告: max-pages=%d に到達。取得は不完全の可能性\n", bu.BoardID, maxPages)
	} else if anchor > 0 && !res.AnchorFound && !res.StoppedOld {
		fmt.Fprintf(os.Stderr, "[%s] 警告: アンカー%d に到達しないまま末尾。全%d件を投入\n",
			bu.BoardID, anchor, len(inserts))
	}

	if len(inserts) == 0 && len(updates) == 0 {
		return 0, 0, "nochange", nil
	}
	if !dryRun {
		tx, err := db.Begin()
		if err != nil {
			return 0, 0, "", err
		}
		defer tx.Rollback()
		insStmt, err := tx.Prepare(
			`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return 0, 0, "", err
		}
		updStmt, err := tx.Prepare(
			`UPDATE threads SET res_count = ? WHERE board_id = ? AND thread_key = ?`)
		if err != nil {
			return 0, 0, "", err
		}
		for _, e := range inserts {
			if _, err := insStmt.Exec(bu.BoardID, e.ThreadKey, e.Title, e.ResCount); err != nil {
				return 0, 0, "", err
			}
		}
		for _, u := range updates {
			if _, err := updStmt.Exec(u.rc, bu.BoardID, u.tk); err != nil {
				return 0, 0, "", err
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, 0, "", err
		}
	}
	fmt.Printf("[%s] 新規%d件 更新%d件 (%dページ)\n", bu.BoardID, len(inserts), len(updates), res.Pages)
	return int64(len(inserts)), int64(len(updates)), "ok", nil
}
