// Package store は SQLite（threads / boards / 除外リスト）へのアクセスを提供する。
// ドライバは pure Go の modernc.org/sqlite を使う（仕様書5章。cgo を使わないことで
// クロスコンパイルと単一バイナリ配置を維持する）。
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open はSQLiteを開く。readOnly は本番API用（仕様書8.2、mode=ro）。
func Open(path string, readOnly bool) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	if readOnly {
		dsn += "&mode=ro&immutable=0"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("SQLite を開けません (%s): %w", path, err)
	}
	return db, nil
}

// boardsSchema のコメントはスキーマ文字列として DB 内に永続化される。
// ★ board_idx は doc_id に焼き込まれるため、一度割り当てたら未来永劫変更禁止（仕様書16.2）。
const boardsSchema = `
CREATE TABLE IF NOT EXISTS boards (
  -- board_idx は doc_id = thread_key*2048 + board_idx に焼き込まれる。
  -- 一度割り当てたら変更禁止。変更は全インデックスの再構築を意味する（仕様書16.2）。
  -- 新規板は必ず末尾（既存最大値+1）に追加すること。
  board_idx    INTEGER PRIMARY KEY,   -- 0〜2047
  board_id     TEXT NOT NULL UNIQUE,  -- 例: livejupiter
  board_name   TEXT,                  -- 表示名。不明なら board_id
  category     TEXT,                  -- 板一覧のグルーピング用（任意）
  thread_count INTEGER DEFAULT 0      -- 統計表示用。再構築時に更新
)`

// EnsureBoards は boards テーブルを作成し、threads に存在する全 board_id に
// board_idx を割り当てる。既存の割り当ては絶対に変更せず、新規板のみ
// 末尾（max(board_idx)+1）へ board_id 昇順で追加する。
func EnsureBoards(db *sql.DB) (added int, total int, err error) {
	if _, err = db.Exec(boardsSchema); err != nil {
		return 0, 0, err
	}

	existing := map[string]bool{}
	nextIdx := int64(0)
	rows, err := db.Query(`SELECT board_id, board_idx FROM boards`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var id string
		var idx int64
		if err = rows.Scan(&id, &idx); err != nil {
			rows.Close()
			return 0, 0, err
		}
		existing[id] = true
		if idx >= nextIdx {
			nextIdx = idx + 1
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}

	// 板ごとの件数集計（フルスキャン。構築マシンでのみ実行する想定）
	type bc struct {
		id  string
		cnt int64
	}
	var all []bc
	rows, err = db.Query(`SELECT board_id, COUNT(*) FROM threads GROUP BY board_id ORDER BY board_id`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var b bc
		if err = rows.Scan(&b.id, &b.cnt); err != nil {
			rows.Close()
			return 0, 0, err
		}
		all = append(all, b)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	for _, b := range all {
		if !existing[b.id] {
			if nextIdx >= 2048 {
				return 0, 0, fmt.Errorf("board_idx が2048枠を超過（board_id=%s）", b.id)
			}
			if _, err = tx.Exec(
				`INSERT INTO boards (board_idx, board_id, board_name, thread_count) VALUES (?, ?, ?, ?)`,
				nextIdx, b.id, b.id, b.cnt); err != nil {
				return 0, 0, err
			}
			nextIdx++
			added++
		} else {
			if _, err = tx.Exec(
				`UPDATE boards SET thread_count = ? WHERE board_id = ?`, b.cnt, b.id); err != nil {
				return 0, 0, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, len(all), nil
}

// LoadBoardIdx は board_id → board_idx の対応表を返す（エクスポート・API 起動時用）。
func LoadBoardIdx(db *sql.DB) (map[string]int64, error) {
	rows, err := db.Query(`SELECT board_id, board_idx FROM boards`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]int64, 1024)
	for rows.Next() {
		var id string
		var idx int64
		if err := rows.Scan(&id, &idx); err != nil {
			return nil, err
		}
		m[id] = idx
	}
	return m, rows.Err()
}
