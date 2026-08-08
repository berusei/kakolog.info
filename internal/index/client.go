package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// QueryTimeout: Manticore クエリのタイムアウト（仕様書12.4）。超過時は API が 503 を返す。
const QueryTimeout = 3 * time.Second

type Client struct {
	db *sql.DB
}

// Open は searchd の MySQL プロトコルポートへ接続する。
// Manticore は MySQL 完全互換ではないため interpolateParams=true が必須
// （サーバーサイドプリペアドを避ける。仕様書10）。
func Open(addr string) (*Client, error) {
	dsn := fmt.Sprintf("tcp(%s)/?interpolateParams=true&parseTime=false&timeout=2s&readTimeout=5s&writeTimeout=5s", addr)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(60 * time.Second)
	return &Client{db: db}, nil
}

func (c *Client) Close() error { return c.db.Close() }

// Ping はヘルスチェック用（仕様書10.3）。
func (c *Client) Ping(ctx context.Context) error {
	return c.db.PingContext(ctx)
}

// Result は検索結果。
type Result struct {
	IDs        []int64
	TotalFound int
	// Approximate: cutoff に達し total_found が概算のとき true（仕様書12.1）
	Approximate bool
}

// Search はクエリを実行し、直後に SHOW META を発行する。
// ★ MATCH と SHOW META は必ず同一コネクションで連続実行する（仕様書11.2）。
// database/sql のプールから *sql.Conn を1本取り出して両方を流すことで保証する。
func (c *Client) Search(ctx context.Context, req Request) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()

	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	query, args := req.SQL()
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		res.IDs = append(res.IDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	meta, err := conn.QueryContext(ctx, "SHOW META")
	if err != nil {
		return nil, err
	}
	defer meta.Close()
	for meta.Next() {
		var name, value string
		if err := meta.Scan(&name, &value); err != nil {
			return nil, err
		}
		switch name {
		case "total_found":
			res.TotalFound, _ = strconv.Atoi(value)
		case "total_relation":
			// Manticore 28 は cutoff 到達時に gte を返す
			if value == "gte" {
				res.Approximate = true
			}
		}
	}
	if err := meta.Err(); err != nil {
		return nil, err
	}
	// cutoff に達した場合は概算（仕様書12.1）。total_relation が無い版への保険。
	// Exact かつ ExactLimit=0（打ち切りなし）のときは total_found が常に正確
	if limit := req.effectiveCutoff(); limit > 0 && res.TotalFound >= limit {
		res.Approximate = true
	}
	return res, nil
}
