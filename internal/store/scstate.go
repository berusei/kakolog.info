package store

import (
	"database/sql"
	"strings"
	"time"
)

// 2ch.sc からの補完（docs/changes/2026-08-21.md）に使う2つのテーブル。
// どちらも threads（1.3億行）には手を触れないための外付けの記録である。
const scStateSchema = `
CREATE TABLE IF NOT EXISTS sc_sourced (
  -- 2ch.sc から取り込んだ行の記録。5ch の倉庫が復旧して同じスレを取り直した時点で
  -- この行は削除する（＝5ch 由来の値が正になった印）。
  -- 残っている行は「まだ5chで裏が取れていない、sc由来のスレ」を意味する。
  board_id   TEXT NOT NULL,
  thread_key INTEGER NOT NULL,
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (board_id, thread_key)
);
CREATE TABLE IF NOT EXISTS sc_kako_state (
  -- sc の過去ログ索引ページに載っている倉庫ごとの格納数のキャッシュ。
  -- ★ 倉庫番号は thread_key の上4桁＝スレの作成時刻であって、アーカイブ時刻ではない。
  -- 長寿スレは古い倉庫番号へ後から追加されるため「新しい倉庫だけ見る」では取りこぼす。
  -- 格納数が前回と変わった倉庫だけ subject.txt を取りに行くための記録。
  board_id   TEXT NOT NULL,
  kako_no    INTEGER NOT NULL,  -- 例: 1787
  count      INTEGER NOT NULL,  -- 索引ページが申告する格納数
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (board_id, kako_no)
)`

// EnsureSCState は sc 補完用のテーブルを作成する。
func EnsureSCState(db *sql.DB) error {
	for _, stmt := range strings.Split(scStateSchema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// LoadSCKakoCounts は1板ぶんの倉庫番号→格納数を返す。
func LoadSCKakoCounts(db *sql.DB, boardID string) (map[int]int, error) {
	rows, err := db.Query(`SELECT kako_no, count FROM sc_kako_state WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int]int{}
	for rows.Next() {
		var no, cnt int
		if err := rows.Scan(&no, &cnt); err != nil {
			return nil, err
		}
		m[no] = cnt
	}
	return m, rows.Err()
}

// SaveSCKakoCount は倉庫1つぶんの格納数を記録する。
// 取り込みに成功した後、または「取得対象外と判断した」時点で呼ぶ。
func SaveSCKakoCount(x execer, boardID string, kakoNo, count int, now time.Time) error {
	_, err := x.Exec(
		`INSERT INTO sc_kako_state (board_id, kako_no, count, fetched_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(board_id, kako_no) DO UPDATE SET
		   count = excluded.count, fetched_at = excluded.fetched_at`,
		boardID, kakoNo, count, now.Unix())
	return err
}

// MarkSCSourced は sc から取り込んだスレを記録する。投入と同一トランザクションで呼ぶこと。
func MarkSCSourced(x execer, boardID string, threadKey int64, now time.Time) error {
	_, err := x.Exec(
		`INSERT INTO sc_sourced (board_id, thread_key, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(board_id, thread_key) DO UPDATE SET fetched_at = excluded.fetched_at`,
		boardID, threadKey, now.Unix())
	return err
}

// ClearSCSourced は「5ch 側で同じスレを取り直した」ことを記録する（行を消す）。
func ClearSCSourced(x execer, boardID string, threadKey int64) error {
	_, err := x.Exec(`DELETE FROM sc_sourced WHERE board_id = ? AND thread_key = ?`,
		boardID, threadKey)
	return err
}

// SCSourcedKeys は1板ぶんの sc 由来 thread_key の集合を返す。
// sc からの再取得で res_count を更新してよいのは、この集合に含まれる行だけである
// （5ch 由来の行に sc のレス数を上書きすると、実測13%が少ない方へ退行する）。
func SCSourcedKeys(db *sql.DB, boardID string) (map[int64]bool, error) {
	rows, err := db.Query(`SELECT thread_key FROM sc_sourced WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]bool{}
	for rows.Next() {
		var tk int64
		if err := rows.Scan(&tk); err != nil {
			return nil, err
		}
		m[tk] = true
	}
	return m, rows.Err()
}

// CountSCSourced は sc 由来でまだ5chの裏が取れていない行数を返す。
// テーブルが無い（旧デプロイDB）場合は 0。API のフッター表示に使う。
func CountSCSourced(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM sc_sourced`).Scan(&n)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return 0, nil
	}
	return n, err
}
