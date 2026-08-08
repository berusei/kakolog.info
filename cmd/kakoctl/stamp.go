package main

import (
	"flag"
	"fmt"
	"time"

	"kakosearch/internal/store"
	"kakosearch/internal/tokenizer"
)

// cmdStamp はインデックス構築直後に実行し、正規化バージョンと構築時刻を
// SQLite の kako_meta に記録する（仕様書16.3）。
// API は起動時にこの値と自身の tokenizer.Version を照合し、不一致なら起動を拒否する。
// 除外リストテーブル（仕様書16.4）もここで作成される。
func cmdStamp(args []string) error {
	fs := flag.NewFlagSet("stamp", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	fs.Parse(args)

	db, err := store.Open(*dbPath, false)
	if err != nil {
		return err
	}
	defer db.Close()

	now := time.Now()
	if err := store.Stamp(db, tokenizer.Version, now); err != nil {
		return err
	}
	fmt.Printf("stamp: normalizer_version=%d index_built_at=%s\n",
		tokenizer.Version, now.Format(time.RFC3339))
	return nil
}
