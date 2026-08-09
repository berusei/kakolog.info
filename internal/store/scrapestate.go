package store

import (
	"database/sql"
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
const scrapeStateSchema = `
CREATE TABLE IF NOT EXISTS scrape_state (
  board_id   TEXT PRIMARY KEY,  -- 例: livejupiter
  latest_at  INTEGER NOT NULL,  -- 取得元ページの Latest update（UNIX秒）
  scanned_at INTEGER NOT NULL   -- この値を記録した時刻（UNIX秒）。診断用
)`

// EnsureScrapeState は scrape_state テーブルを作成する。
func EnsureScrapeState(db *sql.DB) error {
	_, err := db.Exec(scrapeStateSchema)
	return err
}

// LoadScrapeState は board_id → 前回の Latest update を返す。
// 記録が無い板は map に現れない（＝ゼロ値となり、必ず本スキャンされる）。
func LoadScrapeState(db *sql.DB) (map[string]time.Time, error) {
	rows, err := db.Query(`SELECT board_id, latest_at FROM scrape_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]time.Time, 1024)
	for rows.Next() {
		var id string
		var ts int64
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		m[id] = time.Unix(ts, 0)
	}
	return m, rows.Err()
}

// SaveScrapeState は1板分の Latest update を記録する。
// 取り込みが成功した後にだけ呼ぶこと（理由は scrapeStateSchema のコメント）。
// latest がゼロ値（ページに記載が無い板）のときは何も書かない。記録しないことで
// その板は毎回本スキャンされ、従来どおりアンカーで差分を取る動作に留まる。
func SaveScrapeState(x execer, boardID string, latest time.Time, now time.Time) error {
	if latest.IsZero() {
		return nil
	}
	_, err := x.Exec(
		`INSERT INTO scrape_state (board_id, latest_at, scanned_at) VALUES (?, ?, ?)
		 ON CONFLICT(board_id) DO UPDATE SET latest_at = excluded.latest_at, scanned_at = excluded.scanned_at`,
		boardID, latest.Unix(), now.Unix())
	return err
}

// execer は *sql.DB と *sql.Tx の両方を受けるための最小インターフェース。
// 新規投入があるときはトランザクション内で、更新なしのときは DB 直で書く。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}
