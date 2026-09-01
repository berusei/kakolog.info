package main

import (
	"flag"
	"fmt"
	"time"

	"kakosearch/internal/docid"
	"kakosearch/internal/store"
	"kakosearch/internal/tokenizer"
)

// cmdStamp はインデックス構築直後に実行し、正規化バージョンと構築時刻を
// SQLite の kako_meta に記録する（仕様書16.3）。
// API は起動時にこの値と自身の tokenizer.Version を照合し、不一致なら起動を拒否する。
// 除外リストテーブル（仕様書16.4）もここで作成される。
//
// あわせて data_updated_at（DB に入っている最新スレの作成時刻）を記録する。
// フッターに出すべきは「索引をビルドした時刻」ではなく「データがいつまで入っているか」
// である（docs/changes/2026-08-09.md で持ち越した不具合の修正）。
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

	tk, err := store.MaxThreadKey(db, docid.ThreadKeyExcludeMin)
	if err != nil {
		return err
	}
	dataUpdated := ""
	if tk > 0 {
		t := time.Unix(tk, 0)
		dataUpdated = t.UTC().Format(time.RFC3339)
		if err := store.SetMeta(db, "data_updated_at", dataUpdated); err != nil {
			return err
		}
		dataUpdated = t.In(docid.JST).Format("2006-01-02 15:04:05")
	}
	fmt.Printf("stamp: normalizer_version=%d index_built_at=%s data_updated_at=%s\n",
		tokenizer.Version, now.Format(time.RFC3339), dataUpdated)
	return nil
}
