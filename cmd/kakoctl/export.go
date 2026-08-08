package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"kakosearch/internal/docid"
	"kakosearch/internal/store"
	"kakosearch/internal/tokenizer"
)

// cmdExport は Manticore tsvpipe 用の TSV を標準出力へストリーミングする（仕様書7.1）。
// 列: doc_id \t title（トークン化済み・エスケープなし） \t board（b_ プレフィックス付き）
//
// - thread_key >= docid.ThreadKeyExcludeMin の行は除外する（G1決定）
// - 空タイトルは投入する（G1決定。空フィールドとして出力）
// - 1.3億行をメモリに載せない。sql.Rows でストリーミングする
func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	order := fs.String("order", "", "new（kako_new 用）または old（kako_old 用）")
	board := fs.String("board", "", "指定した板のみ出力（小規模テスト用）")
	fs.Parse(args)

	if *order != "new" && *order != "old" {
		return fmt.Errorf("--order は new または old を指定してください")
	}

	db, err := store.Open(*dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()

	boardIdx, err := store.LoadBoardIdx(db)
	if err != nil {
		return fmt.Errorf("boards テーブルの読み込みに失敗（kakoctl boards を先に実行すること）: %w", err)
	}
	if len(boardIdx) == 0 {
		return fmt.Errorf("boards テーブルが空です。kakoctl boards を先に実行してください")
	}

	// idx_threads_key(thread_key, board_id) により、この ORDER BY はソート済み
	// インデックス走査になる。初回割り当てでは board_idx は board_id 昇順なので、
	// この順序は doc_id 順とほぼ一致する（indexer は内部で再整列するため、
	// 完全一致でなくてよい。昇順に近いほどビルドが軽くなるだけ。仕様書7.2）。
	q := `SELECT board_id, thread_key, title FROM threads`
	var qargs []any
	if *board != "" {
		q += ` WHERE board_id = ?`
		qargs = append(qargs, *board)
	}
	if *order == "new" {
		q += ` ORDER BY thread_key DESC, board_id DESC`
	} else {
		q += ` ORDER BY thread_key ASC, board_id ASC`
	}

	rows, err := db.Query(q, qargs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	w := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer w.Flush()

	var written, skippedKey int64
	buf := make([]byte, 0, 4096)
	for rows.Next() {
		var bid, title string
		var tk int64
		if err := rows.Scan(&bid, &tk, &title); err != nil {
			return err
		}
		if tk < 0 || tk >= docid.ThreadKeyExcludeMin {
			skippedKey++
			continue
		}
		bidx, ok := boardIdx[bid]
		if !ok {
			// boards は threads から生成するため、ここに来たら割り当て漏れ。
			// 件数一致検証（7.4）が狂うので黙って捨てずに停止する。
			return fmt.Errorf("board_id %q が boards テーブルにありません。kakoctl boards を再実行してください", bid)
		}
		var id int64
		if *order == "new" {
			id = docid.New(tk, bidx)
		} else {
			id = docid.Asc(tk, bidx)
		}
		tok := tokenizer.IndexText(title)
		// TSV 安全策（仕様書7.1）: トークナイザが制御文字を除去するため通常は
		// 到達しないが、indexer を沈黙させないため最終ガードを置く
		if strings.ContainsAny(tok, "\t\n\r\x00") {
			tok = strings.Map(func(r rune) rune {
				if r == '\t' || r == '\n' || r == '\r' || r == 0 {
					return -1
				}
				return r
			}, tok)
		}
		buf = strconv.AppendInt(buf[:0], id, 10)
		buf = append(buf, '\t')
		buf = append(buf, tok...)
		buf = append(buf, '\t', 'b', '_')
		buf = append(buf, bid...)
		buf = append(buf, '\n')
		if _, err := w.Write(buf); err != nil {
			return err
		}
		written++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "export --order %s: %d行出力、thread_key範囲外の除外 %d行\n", *order, written, skippedKey)
	return nil
}
