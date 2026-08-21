package store

import (
	"database/sql"
	"strings"
	"time"
)

// scrapeStateSchema のコメントはスキーマ文字列として DB 内に永続化される。
//
// latest_at は取得元の kako0000.html が申告する「Latest update」であって、
// こちらがスキャンした時刻ではない。次回スキャン時にこの値と比較し、進んでいなければ
// その板の取得を丸ごと省く。
//
// ★ この行は「その板の取り込みがコミットまで成功した」ことの記録である。
// スキャンや投入が途中で失敗した板は値を据え置くこと。据え置けば次回必ず再試行され、
// 取りこぼしが原理的に起きない。失敗しても更新してしまうと、取得元のページが
// その後更新されない限りその板は永久にスキップされる。
//
// ★★ anchor_5ch は「5ch の一覧ページで最後に確認した最新 thread_key」である。
// 5ch のスキャンは必ずこの値をアンカーに使うこと（threads の MAX を使ってはならない）。
// 2ch.sc から補完した行（docs/changes/2026-08-21.md）が threads に入ると MAX が
// 先へ飛び、5ch の倉庫が復旧しても停止期間ぶんを取りに行かなくなるため。
const scrapeStateSchema = `
CREATE TABLE IF NOT EXISTS scrape_state (
  board_id   TEXT PRIMARY KEY,  -- 例: livejupiter
  latest_at  INTEGER NOT NULL,  -- 取得元ページの Latest update（UNIX秒）
  scanned_at INTEGER NOT NULL,  -- この値を記録した時刻（UNIX秒）。診断用
  anchor_5ch INTEGER            -- 5ch の一覧で最後に見た最新 thread_key。NULL は未記録
)`

// BoardState は1板分のスクレイプ状態。
type BoardState struct {
	LatestAt  time.Time // 5ch の一覧ページが申告する Latest update。ゼロ値 = 未記録
	Anchor5ch int64     // 5ch の一覧で最後に見た最新 thread_key。0 = 未記録
}

// EnsureScrapeState は scrape_state テーブルを作成する。
// 既存 DB（anchor_5ch を持たない世代）には列を追加する。
func EnsureScrapeState(db *sql.DB) error {
	if _, err := db.Exec(scrapeStateSchema); err != nil {
		return err
	}
	// SQLite に IF NOT EXISTS 付きの ADD COLUMN は無いので、重複エラーを許容する
	if _, err := db.Exec(`ALTER TABLE scrape_state ADD COLUMN anchor_5ch INTEGER`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

// LoadScrapeState は board_id → 状態を返す。
// 記録が無い板は map に現れない（＝ゼロ値となり、必ず本スキャンされる）。
func LoadScrapeState(db *sql.DB) (map[string]BoardState, error) {
	rows, err := db.Query(`SELECT board_id, latest_at, COALESCE(anchor_5ch, 0) FROM scrape_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]BoardState, 1024)
	for rows.Next() {
		var id string
		var ts, anchor int64
		if err := rows.Scan(&id, &ts, &anchor); err != nil {
			return nil, err
		}
		st := BoardState{Anchor5ch: anchor}
		if ts > 0 {
			st.LatestAt = time.Unix(ts, 0)
		}
		m[id] = st
	}
	return m, rows.Err()
}

// SaveScrapeState は1板分の状態を記録する。
// 取り込みが成功した後にだけ呼ぶこと（理由は scrapeStateSchema のコメント）。
//
// latest がゼロ値（ページに Latest update が無い板）のときは latest_at を書かない。
// 記録しないことでその板は毎回本スキャンされ、従来どおりアンカーで差分を取る動作に留まる。
// anchor が 0 のときも同様に anchor_5ch を書かない。どちらも指定が無ければ何もしない。
func SaveScrapeState(x execer, boardID string, latest time.Time, anchor int64, now time.Time) error {
	var ts int64
	if !latest.IsZero() {
		ts = latest.Unix()
	}
	if ts == 0 && anchor == 0 {
		return nil
	}
	_, err := x.Exec(
		`INSERT INTO scrape_state (board_id, latest_at, scanned_at, anchor_5ch) VALUES (?, ?, ?, ?)
		 ON CONFLICT(board_id) DO UPDATE SET
		   latest_at  = CASE WHEN excluded.latest_at  > 0 THEN excluded.latest_at  ELSE scrape_state.latest_at  END,
		   anchor_5ch = CASE WHEN excluded.anchor_5ch > 0 THEN excluded.anchor_5ch ELSE scrape_state.anchor_5ch END,
		   scanned_at = excluded.scanned_at`,
		boardID, ts, now.Unix(), anchor)
	return err
}

// execer は *sql.DB と *sql.Tx の両方を受けるための最小インターフェース。
// 新規投入があるときはトランザクション内で、更新なしのときは DB 直で書く。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}
