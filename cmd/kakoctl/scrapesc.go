// scrapesc.go は 2ch.sc からの補完（5ch の過去ログ倉庫が止まっている板向け）。
// 設計と実測は docs/changes/2026-08-21.md。呼び出し元は scrape.go。
package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"kakosearch/internal/scrape"
	"kakosearch/internal/store"
)

// scResult は1板分の sc 補完の結果。
type scResult struct {
	nNew, nUpd  int64
	dirsFetched int    // subject.txt を実際に取得した倉庫数
	dirsSkipped int    // 格納数が前回と同じで取得を省いた倉庫数
	status      string // "ok" / "404" / "nochange"
}

// scrapeBoardSC は1板分を 2ch.sc の過去ログ倉庫から補完する。
//
// anchor は 5ch 用アンカー（＝5ch から取り込めている最新 thread_key）。
// 初回は「その倉庫番号以降」だけを取得対象にする。板あたり500個ある倉庫を
// 全部取りに行くと833板で40万リクエストになるため、停止期間ぶんに絞る（設計判断6）。
//
// 2回目以降は索引ページの格納数と sc_kako_state の記録を突き合わせ、
// 数が変わった倉庫だけを取りに行く。倉庫番号はスレの作成時刻であってアーカイブ時刻では
// ないため、古い倉庫に後から追加されるスレはこの比較でしか拾えない。
func scrapeBoardSC(db *sql.DB, client *scrape.Client, existsStmt *sql.Stmt,
	boardID, base string, anchor int64, maxDirs int, dryRun bool) (scResult, error) {

	var out scResult
	counts, err := store.LoadSCKakoCounts(db, boardID)
	if err != nil {
		return out, err
	}
	dirs, notFound, err := client.FetchKakoIndex(base)
	if err != nil {
		return out, err
	}
	if notFound || len(dirs) == 0 {
		out.status = "404"
		return out, nil
	}

	minKako := 0
	if anchor > 0 {
		minKako = scrape.KakoNoOf(anchor)
	} else {
		// その板の行がまだ1件も無い。全部は取らず最新の倉庫だけを見る
		for _, d := range dirs {
			if d.No > minKako {
				minKako = d.No
			}
		}
	}

	var targets, baseline []scrape.KakoDir
	for _, d := range dirs {
		prev, known := counts[d.No]
		switch {
		case known && d.Count >= 0 && prev == d.Count:
			out.dirsSkipped++ // 格納数が動いていない＝新しいスレは入っていない
		case !known && d.No < minKako:
			// 初回に見えた古い倉庫。中身は 5ch から取り込み済みのはずなので取得せず、
			// 格納数だけ基準値として記録する（以後は変化したときだけ取りに行く）
			baseline = append(baseline, d)
		default:
			targets = append(targets, d)
		}
	}
	// 索引ページは新しい順に並ぶ。上限に掛かったぶんは記録を残さず、次回に回す
	capped := false
	if maxDirs > 0 && len(targets) > maxDirs {
		targets = targets[:maxDirs]
		capped = true
	}

	scKeys, err := store.SCSourcedKeys(db, boardID)
	if err != nil {
		return out, err
	}

	type upd struct{ tk, rc int64 }
	var inserts []scrape.Entry
	var updates []upd
	var fetched []scrape.KakoDir
	seen := map[int64]bool{}
	for _, d := range targets {
		entries, nf, err := client.FetchSubject(base, d.No)
		if err != nil {
			// 1つの倉庫の失敗で板全体を落とさない。この倉庫は記録しないので次回再試行される
			fmt.Fprintf(os.Stderr, "[%s] sc 警告: o%d の取得に失敗: %v\n", boardID, d.No, err)
			continue
		}
		if nf {
			continue
		}
		fetched = append(fetched, d)
		out.dirsFetched++
		for _, e := range entries {
			if seen[e.ThreadKey] {
				continue
			}
			seen[e.ThreadKey] = true
			var rc int64
			serr := existsStmt.QueryRow(boardID, e.ThreadKey).Scan(&rc)
			switch {
			case serr == sql.ErrNoRows:
				inserts = append(inserts, e)
			case serr != nil:
				return out, serr
			case scKeys[e.ThreadKey] && rc != e.ResCount:
				// ★ res_count を上書きしてよいのは sc 由来の行だけ。
				// 5ch 由来の行に sc の値を被せると、実測13%が少ない方へ退行する
				updates = append(updates, upd{e.ThreadKey, e.ResCount})
			}
		}
	}

	out.nNew = int64(len(inserts))
	out.nUpd = int64(len(updates))
	out.status = "ok"
	if len(inserts) == 0 && len(updates) == 0 {
		out.status = "nochange"
	}

	if dryRun {
		if out.dirsFetched > 0 || out.nNew > 0 {
			fmt.Printf("[%s] sc: 新規%d件 更新%d件（倉庫%d件取得 / %d件スキップ%s）\n",
				boardID, out.nNew, out.nUpd, out.dirsFetched, out.dirsSkipped, cappedNote(capped))
		}
		return out, nil
	}

	now := time.Now()
	tx, err := db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	insStmt, err := tx.Prepare(
		`INSERT INTO threads (board_id, thread_key, title, res_count) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return out, err
	}
	updStmt, err := tx.Prepare(
		`UPDATE threads SET res_count = ? WHERE board_id = ? AND thread_key = ?`)
	if err != nil {
		return out, err
	}
	for _, e := range inserts {
		if _, err := insStmt.Exec(boardID, e.ThreadKey, e.Title, e.ResCount); err != nil {
			return out, err
		}
		// 由来の記録。5ch で取り直せた時点でこの行は消える（scrape.go）
		if err := store.MarkSCSourced(tx, boardID, e.ThreadKey, now); err != nil {
			return out, err
		}
	}
	for _, u := range updates {
		if _, err := updStmt.Exec(u.rc, boardID, u.tk); err != nil {
			return out, err
		}
	}
	// ★ 格納数の記録は投入と同一トランザクションに入れる。別々にすると
	// 「記録だけ進んで中身が入っていない」倉庫が生まれ、その差分が永久に埋まらない。
	for _, d := range append(fetched, baseline...) {
		if d.Count < 0 {
			continue // 格納数が読めなかった倉庫。記録せず毎回取得する
		}
		if err := store.SaveSCKakoCount(tx, boardID, d.No, d.Count, now); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}

	if out.nNew > 0 || out.nUpd > 0 {
		fmt.Printf("[%s] sc: 新規%d件 更新%d件（倉庫%d件取得 / %d件スキップ%s）\n",
			boardID, out.nNew, out.nUpd, out.dirsFetched, out.dirsSkipped, cappedNote(capped))
	}
	return out, nil
}

func cappedNote(capped bool) string {
	if capped {
		return " / 上限に達したため一部を次回へ繰り越し"
	}
	return ""
}
