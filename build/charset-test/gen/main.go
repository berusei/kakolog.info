// charset_table 検証用（仕様書6.4）: 5.4の全テストケースの文字を含むダミー文書を
// 本物の tokenizer で TSV 化し、検証対象トークン一覧（chars.txt）を出力する。
package main

import (
	"fmt"
	"os"
	"sort"

	"kakosearch/internal/tokenizer"
)

var docs = []struct {
	id    int
	title string
	board string
}{
	{1, "チーズケーキ", "livejupiter"},
	{2, "チズ", "livejupiter"},
	{3, "ドラゴンボールZ", "119"},
	{4, "ﾊﾟｿｺﾝ", "2chbook"},
	{5, "①②③", "livejupiter"},
	{6, "㍿", "119"},
	{7, "【速報】（笑）", "livejupiter"},
	{8, "!!!？？", "2chbook"},
	{9, "𠮷野家", "livejupiter"},
	{10, "スレ👨‍👩‍👧", "119"},
	{11, "DragonBall", "livejupiter"},
	{12, "ー・〜♪★☆", "2chbook"},
	{13, "チーズ", "livejupiter"},
	{14, "速報�です", "119"},
	{15, `A\B(C)|D-E!F@G~H"I&J/K^L$M=N<O>P`, "livejupiter"},
	{16, "つけ麺と味噌ラーメン", "2chbook"},
}

func main() {
	outDir := "."
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	tsv, err := os.Create(outDir + "/test.tsv")
	if err != nil {
		panic(err)
	}
	defer tsv.Close()

	tokens := map[string]bool{}
	for _, d := range docs {
		it := tokenizer.IndexText(d.title)
		fmt.Fprintf(tsv, "%d\t%s\tb_%s\n", d.id, it, d.board)
		for _, t := range tokenizer.Tokens(d.title) {
			tokens[t] = true
		}
		tokens["b_"+d.board] = true
	}

	var list []string
	for t := range tokens {
		list = append(list, t)
	}
	sort.Strings(list)
	chars, err := os.Create(outDir + "/chars.txt")
	if err != nil {
		panic(err)
	}
	defer chars.Close()
	for _, t := range list {
		fmt.Fprintln(chars, t)
	}
	fmt.Fprintf(os.Stderr, "%d docs, %d unique tokens\n", len(docs), len(list))
}
