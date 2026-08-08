package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"kakosearch/internal/docid"
	"kakosearch/internal/index"
	"kakosearch/internal/store"
)

// 一覧モード（検索語なし）を出せない (板, 年) の組を書き出す。
//
// 目的は「サーバーに聞きに行かなくても、押される前に弾く」こと（依頼者提案 2026-08-06）。
// 出力はフロントが読む静的ファイルで、nginx が配信するため searchd も SQLite も動かない。
//
// ★ これは UI のヒントであって、可用性の保証ではない。件数の出どころは SQLite
// （スクレイパーが更新し続ける）だが、一覧の実体は Manticore の索引（月次再構築）である。
// 当年ぶんは必ずズレるため、cmd/kakoapi 側の 400 too_many_results を消してはならない。
// このファイルが古くても、最悪「弾かれるはずのものが1回サーバーまで届く」で済む。
const (
	// 走査対象の候補。総数が窓以下の板は、どの単年も窓を超えようがないので調べる必要がない。
	// 902板中29板まで落ちる（2026-08-06 実測）。この絞り込みは近似ではなく、
	// 「単年 ≦ 総数」から導かれるので漏れが出ない。
	byFirstYear = 1999
)

type boardYears struct {
	GeneratedAt string              `json:"generated_at"`
	Window      int                 `json:"window"`
	Blocked     map[string][]int    `json:"blocked"`        // board_id → 一覧できない年
	BlockedMon  map[string][]string `json:"blocked_months"` // board_id → 一覧できない月（"YYYY-MM"）
}

func cmdBoardYears(args []string) error {
	fs := flag.NewFlagSet("board-years", flag.ExitOnError)
	dbPath := fs.String("db", "./db/kakolog.db", "SQLite ファイルのパス")
	out := fs.String("out", "./web/dist/board-years.json", "出力先 JSON")
	window := fs.Int("window", index.MaxResultWindow, "一覧できる上限件数（API の MaxResultWindow と揃えること）")
	fs.Parse(args)

	db, err := store.Open(*dbPath, true) // 読み取りのみ。本番で流しても書き込まない
	if err != nil {
		return err
	}
	defer db.Close()

	// 候補板の抽出（総数が窓を超える板だけ）
	rows, err := db.Query(`SELECT board_id FROM boards WHERE thread_count > ? ORDER BY board_id`, *window)
	if err != nil {
		return err
	}
	var cands []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	lastYear := time.Now().In(docid.JST).Year()
	fmt.Fprintf(os.Stderr, "board-years: 候補 %d 板 × %d年 を集計します\n", len(cands), lastYear-byFirstYear+1)

	count := func(b string, lo, hi int64) (int, error) {
		var c int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM threads WHERE board_id=? AND thread_key>=? AND thread_key<?`,
			b, lo, hi).Scan(&c)
		return c, err
	}

	// (板, 年) ごとに件数を数える。idx_threads_lookup(board_id, thread_key) の
	// レンジスキャンになるのでテーブル本体は読まない（実測: 29板で約24秒）。
	blocked := map[string][]int{}
	blockedMon := map[string][]string{}
	for _, b := range cands {
		for y := byFirstYear; y <= lastYear; y++ {
			lo := time.Date(y, 1, 1, 0, 0, 0, 0, docid.JST).Unix()
			hi := time.Date(y+1, 1, 1, 0, 0, 0, 0, docid.JST).Unix()
			c, err := count(b, lo, hi)
			if err != nil {
				return fmt.Errorf("%s %d年 の集計に失敗: %w", b, y, err)
			}
			if c <= *window {
				continue
			}
			blocked[b] = append(blocked[b], y)

			// 月の内訳は「年が窓を超えた」ものだけ数えれば足りる。
			// 単月の件数はその年の件数以下なので、年が窓以下なら月も超えようがない。
			// これは近似ではなく単調性から導かれるので漏れが出ない（候補板の絞り込みと同じ理屈）。
			for m := 1; m <= 12; m++ {
				mlo := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, docid.JST).Unix()
				mhi := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, docid.JST).AddDate(0, 1, 0).Unix()
				mc, err := count(b, mlo, mhi)
				if err != nil {
					return fmt.Errorf("%s %d-%02d の集計に失敗: %w", b, y, m, err)
				}
				if mc > *window {
					blockedMon[b] = append(blockedMon[b], fmt.Sprintf("%d-%02d", y, m))
				}
			}
		}
	}

	// 日単位は記録しない。1日で窓を超えるには1板で25万スレが立つ必要があり、
	// 最盛期の news4vip（年477万件）でも1日あたり約1.3万件で、桁が2つ足りない。
	// よって「日まで指定された一覧」は常に事前判定を通す（サーバー側の400が最終判定）。

	total, totalMon := 0, 0
	names := make([]string, 0, len(blocked))
	for b, ys := range blocked {
		sort.Ints(ys)
		total += len(ys)
		names = append(names, b)
	}
	for _, ms := range blockedMon {
		sort.Strings(ms)
		totalMon += len(ms)
	}
	sort.Strings(names)

	data := boardYears{
		GeneratedAt: time.Now().In(docid.JST).Format(time.RFC3339),
		Window:      *window,
		Blocked:     blocked,
		BlockedMon:  blockedMon,
	}
	j, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(j, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "board-years: %d 板 / 年 %d 組 / 月 %d 組が一覧不可 → %s\n",
		len(blocked), total, totalMon, *out)
	for _, b := range names {
		fmt.Fprintf(os.Stderr, "  %s: 年 %v / 月 %d件\n", b, blocked[b], len(blockedMon[b]))
	}
	return nil
}
