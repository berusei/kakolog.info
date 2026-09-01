package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Board は板マスタの1行（仕様書6.2）。
type Board struct {
	BoardIdx    int64  `json:"-"`
	BoardID     string `json:"board_id"`
	BoardName   string `json:"board_name"`
	Category    string `json:"category,omitempty"`
	ThreadCount int64  `json:"thread_count"`
}

// AllBoards は板一覧を thread_count 降順で返す（仕様書10.2）。
func AllBoards(db *sql.DB) ([]Board, error) {
	rows, err := db.Query(`SELECT board_idx, board_id, COALESCE(board_name, board_id),
		COALESCE(category, ''), thread_count FROM boards ORDER BY thread_count DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.BoardIdx, &b.BoardID, &b.BoardName, &b.Category, &b.ThreadCount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateBoardNames は {board_id: {name, category}} 形式の JSON を boards テーブルへ反映する。
// board_idx には一切触らない（仕様書16.2）。JSON に無い板は board_name = board_id のまま残る。
func UpdateBoardNames(db *sql.DB, jsonPath string) (updated, missing int, err error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return 0, 0, err
	}
	var meta map[string]struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return 0, 0, fmt.Errorf("%s のパースに失敗: %w", jsonPath, err)
	}
	boards, err := AllBoards(db)
	if err != nil {
		return 0, 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, b := range boards {
		m, ok := meta[b.BoardID]
		if !ok || m.Name == "" {
			missing++
			continue
		}
		if _, err := tx.Exec(`UPDATE boards SET board_name = ?, category = ? WHERE board_id = ?`,
			m.Name, m.Category, b.BoardID); err != nil {
			return 0, 0, err
		}
		updated++
	}
	return updated, missing, tx.Commit()
}

// Thread は表示用のスレッド1件。
type Thread struct {
	BoardID  string
	Title    string
	ResCount int64
}

// LookupThread は (board_id, thread_key) のポイント参照（仕様書4.3）。
// idx_threads_lookup により1件数十マイクロ秒。見つからなければ nil。
func LookupThread(ctx context.Context, db *sql.DB, boardID string, threadKey int64) (*Thread, error) {
	var t Thread
	t.BoardID = boardID
	err := db.QueryRowContext(ctx,
		`SELECT title, res_count FROM threads WHERE board_id = ? AND thread_key = ?`,
		boardID, threadKey).Scan(&t.Title, &t.ResCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// --- メタ情報（仕様書16.3: 正規化バージョン照合） ---

const metaSchema = `CREATE TABLE IF NOT EXISTS kako_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`

// Stamp はインデックス構築直後に呼び、正規化バージョンと構築時刻を記録する。
// 再構築フロー（deploy/rebuild）で必ずセットで実行すること。
func Stamp(db *sql.DB, normalizerVersion int, builtAt time.Time) error {
	if _, err := db.Exec(metaSchema); err != nil {
		return err
	}
	if _, err := db.Exec(exclusionSchema); err != nil {
		return err
	}
	up := `INSERT INTO kako_meta (key, value) VALUES (?, ?)
	       ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	if _, err := db.Exec(up, "normalizer_version", fmt.Sprint(normalizerVersion)); err != nil {
		return err
	}
	_, err := db.Exec(up, "index_built_at", builtAt.UTC().Format(time.RFC3339))
	return err
}

// SetMeta はメタ値を1件書き込む。kako_meta が無ければ作る。
// スクレイパーが「sc から補完した板数」等を記録し、API のフッター表示に使う。
func SetMeta(x execer, key, value string) error {
	if _, err := x.Exec(metaSchema); err != nil {
		return err
	}
	_, err := x.Exec(`INSERT INTO kako_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta はメタ値を返す。テーブルや行が無ければ空文字。
func Meta(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM kako_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return "", nil
	}
	return v, err
}

// --- 除外リスト（仕様書16.4: 削除依頼対応） ---

const exclusionSchema = `CREATE TABLE IF NOT EXISTS excluded_threads (
  board_id   TEXT NOT NULL,
  thread_key INTEGER NOT NULL,
  note       TEXT,
  PRIMARY KEY (board_id, thread_key)
)`

// LoadExclusions は除外リストを読み込む。キーは "board_id/thread_key"。
// インデックス再構築を待たず即時に検索結果から消すための API 側フィルタ。
// テーブルが無い（旧デプロイDB）場合は空として扱う。
func LoadExclusions(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT board_id, thread_key FROM excluded_threads`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var b string
		var tk int64
		if err := rows.Scan(&b, &tk); err != nil {
			return nil, err
		}
		m[fmt.Sprintf("%s/%d", b, tk)] = true
	}
	return m, rows.Err()
}

// MaxThreadKey は DB に入っているスレッドのうち最も新しい thread_key を返す。
//
// 「インデックス最終更新」としてビルド時刻を表示していたのは誤りだった。
// 取得元が止まっていても再構築は毎回走るため、データが2週間古くても「今日更新」と
// 表示されてしまう（docs/changes/2026-08-09.md で未修正のまま持ち越した不具合）。
// 利用者に見せるべきは「データがいつまで入っているか」であり、それがこの値である。
//
// threads のフルスキャンは避け、boards の各板について MAX を引く
// （idx_threads_lookup(board_id, thread_key) により1板あたり定数時間）。
// 未来日付の壊れたデータ（G1決定、docs/data-audit.md）は除外する。
func MaxThreadKey(db *sql.DB, excludeFrom int64) (int64, error) {
	boards, err := AllBoards(db)
	if err != nil {
		return 0, err
	}
	stmt, err := db.Prepare(
		`SELECT COALESCE(MAX(thread_key), 0) FROM threads WHERE board_id = ? AND thread_key < ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var max int64
	for _, b := range boards {
		var tk int64
		if err := stmt.QueryRow(b.BoardID, excludeFrom).Scan(&tk); err != nil {
			return 0, err
		}
		if tk > max {
			max = tk
		}
	}
	return max, nil
}
