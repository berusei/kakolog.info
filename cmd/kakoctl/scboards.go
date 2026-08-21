// scboards: 2ch.sc の BBSMENU から「板 → sc の過去ログ倉庫URL」の対応表を作る。
// 出力した JSON は kakoctl scrape --sc-urls が読む（docs/changes/2026-08-21.md）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"kakosearch/internal/scrape"
)

func cmdSCBoards(args []string) error {
	fs := flag.NewFlagSet("scboards", flag.ExitOnError)
	urlsPath := fs.String("urls", "./db/board-urls.json", "こちらが持つ板の一覧（5ch側の対応表）")
	out := fs.String("out", "./db/board-urls-sc.json", "出力先")
	menuURL := fs.String("menu", "https://www.2ch.sc/bbsmenu.html", "BBSMENU の URL")
	fs.Parse(args)

	ours, err := loadBoardURLs(*urlsPath)
	if err != nil {
		return err
	}

	client := scrape.New(0)
	hosts, err := client.FetchBBSMenu(*menuURL)
	if err != nil {
		return fmt.Errorf("BBSMENU の取得に失敗: %w", err)
	}
	if len(hosts) == 0 {
		return fmt.Errorf("BBSMENU から板を1つも読み取れませんでした（ページの形式が変わった可能性）")
	}

	// ★ こちらが持つ板とだけ突き合わせる。sc 固有の板は取り込まない
	// （board_idx は 5ch 側の板に対して割り当て済みであり、増やす話とは別問題）。
	var list []boardURL
	var missing []string
	for _, b := range ours {
		host, ok := hosts[b.BoardID]
		if !ok {
			missing = append(missing, b.BoardID)
			continue
		}
		list = append(list, boardURL{
			BoardID: b.BoardID,
			URL:     fmt.Sprintf("https://%s/%s/kako/", host, b.BoardID),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].BoardID < list[j].BoardID })

	buf, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// 一時ファイル経由で置き換える。書き込み中に scrape が読んでも壊れないように
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}

	fmt.Printf("scboards: %s に %d板を書き出しました（BBSMENU %d件 / こちらの板 %d件 / sc に無い板 %d件）\n",
		*out, len(list), len(hosts), len(ours), len(missing))
	if len(missing) > 0 {
		n := len(missing)
		if n > 10 {
			missing = missing[:10]
		}
		fmt.Printf("scboards: sc に無い板（フォールバック対象外）%d件: %v...\n", n, missing)
	}
	return nil
}
