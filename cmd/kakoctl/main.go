// kakoctl は構築マシンで実行する構築・検証ツール（仕様書7章）。
// サブコマンド: boards / export / verify / audit
package main

import (
	"flag"
	"fmt"
	"os"

	"kakosearch/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "boards":
		err = cmdBoards(os.Args[2:])
	case "export":
		err = cmdExport(os.Args[2:])
	case "stamp":
		err = cmdStamp(os.Args[2:])
	case "scrape":
		err = cmdScrape(os.Args[2:])
	case "board-years":
		err = cmdBoardYears(os.Args[2:])
	case "verify":
		err = fmt.Errorf("verify: 7.4 の検証は docs/index-size.md の手順で実施済み")
	case "audit":
		err = fmt.Errorf("audit はフェーズ0で手動実施済み（docs/data-audit.md）")
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kakoctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: kakoctl <boards|export|scrape|stamp|board-years|verify|audit> [flags]
  boards --db <path>   boards テーブルを作成し board_idx を割り当てる
  export --db <path> --order <new|old>   インデックス用 TSV を標準出力へ
  scrape --db <path> --urls <board-urls.json>   過去ログ一覧から新規スレを取得
  stamp  --db <path>   正規化バージョンと構築時刻を kako_meta に記録
  board-years --db <path> --out <json>   検索語なしの一覧ができない(板,年)を書き出す`)
}

func cmdBoards(args []string) error {
	fs := flag.NewFlagSet("boards", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	names := fs.String("names", "", "板の表示名・カテゴリ JSON（{board_id:{name,category}}）。指定時は board_name / category を更新")
	fs.Parse(args)

	db, err := store.Open(*dbPath, false)
	if err != nil {
		return err
	}
	defer db.Close()

	added, total, err := store.EnsureBoards(db)
	if err != nil {
		return err
	}
	fmt.Printf("boards: 全%d板、新規割り当て%d板\n", total, added)

	if *names != "" {
		updated, missing, err := store.UpdateBoardNames(db, *names)
		if err != nil {
			return err
		}
		fmt.Printf("boards: 表示名を%d板に反映、JSONに無い板 %d件（board_id のまま）\n", updated, missing)
	}
	return nil
}
