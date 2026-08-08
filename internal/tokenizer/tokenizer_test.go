package tokenizer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type goldenCase struct {
	Name      string `json:"name"`
	Input     string `json:"input"`
	IndexText string `json:"index_text"`
}

func loadGolden(t *testing.T) []goldenCase {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("golden.json 読み込み失敗: %v", err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatalf("golden.json パース失敗: %v", err)
	}
	return cases
}

func TestGolden(t *testing.T) {
	for _, c := range loadGolden(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := IndexText(c.Input)
			if got != c.IndexText {
				t.Errorf("IndexText(%q) = %q, want %q", c.Input, got, c.IndexText)
			}
		})
	}
}

// hasPhrase は Manticore のフレーズ検索（位置が連続するトークン列）を模擬する。
func hasPhrase(indexText, query string) bool {
	p := strings.Join(Tokens(query), " ")
	if p == "" {
		return false
	}
	return strings.Contains(" "+indexText+" ", " "+p+" ")
}

func TestPhraseSemantics(t *testing.T) {
	idx := IndexText("チーズケーキ")

	if !hasPhrase(idx, "チーズ") {
		t.Error("チーズケーキ は チーズ でヒットすべき")
	}
	// 今回の元凶の回帰テスト（仕様書5.4）
	if hasPhrase(idx, "チズ") {
		t.Error("チーズケーキ は チズ でヒットしてはならない")
	}
	// 語の途中からの部分一致
	if !hasPhrase(idx, "ズケ") {
		t.Error("チーズケーキ は ズケ でヒットすべき")
	}
	if !hasPhrase(IndexText("DragonBall"), "ragon") {
		t.Error("DragonBall は ragon でヒットすべき")
	}
	// 大文字小文字・全角の吸収がクエリ側でも効く
	if !hasPhrase(IndexText("ドラゴンボールZ"), "ボールｚ") {
		t.Error("ドラゴンボールZ は ボールｚ でヒットすべき")
	}
}

func TestEscapeToken(t *testing.T) {
	cases := map[string]string{
		`!`:  `\!`,
		`-`:  `\-`,
		`@`:  `\@`,
		`"`:  `\"`,
		`\`:  `\\`,
		`(`:  `\(`,
		`)`:  `\)`,
		`|`:  `\|`,
		`~`:  `\~`,
		`&`:  `\&`,
		`/`:  `\/`,
		`^`:  `\^`,
		`$`:  `\$`,
		`=`:  `\=`,
		`<`:  `\<`,
		`>`:  `>`, // 予約リスト外はそのまま
		`?`:  `?`,
		`チ`: `チ`,
	}
	for in, want := range cases {
		if got := EscapeToken(in); got != want {
			t.Errorf("EscapeToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueryTokens(t *testing.T) {
	// 全角！は NFKC で ! になり、予約文字としてエスケープされる
	got := QueryTokens("！！！")
	want := []string{`\!`, `\!`, `\!`}
	if len(got) != len(want) {
		t.Fatalf("QueryTokens(！！！) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("QueryTokens(！！！)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTruncation(t *testing.T) {
	long := strings.Repeat("あ", 1200)
	toks := Tokens(long)
	if len(toks) != TitleMax {
		t.Errorf("1200文字は %d トークンに打ち切られるべき、got %d", TitleMax, len(toks))
	}
}

func TestEmptyInputs(t *testing.T) {
	for _, in := range []string{"", " ", "  　\t\n", "�", "\x00\x01"} {
		if toks := Tokens(in); toks != nil {
			t.Errorf("Tokens(%q) = %v, want nil", in, toks)
		}
		if it := IndexText(in); it != "" {
			t.Errorf("IndexText(%q) = %q, want empty", in, it)
		}
	}
}

func TestGraphemeClusters(t *testing.T) {
	toks := Tokens("𠮷野家")
	if len(toks) != 3 || toks[0] != "𠮷" {
		t.Errorf("𠮷野家 は [𠮷 野 家] の3トークンであるべき、got %v", toks)
	}
	toks = Tokens("👨‍👩‍👧スレ")
	if len(toks) != 3 || toks[0] != "👨‍👩‍👧" {
		t.Errorf("ZWJ絵文字は1トークンであるべき、got %v", toks)
	}
}

func TestFFFDNeighborsSearchable(t *testing.T) {
	// U+FFFD を含むタイトルは、その文字を除いた周辺文字で検索できる（仕様書5.4）
	idx := IndexText("速報�です")
	if !hasPhrase(idx, "速報です") {
		t.Errorf("U+FFFD 除去後の連結で検索できるべき: %q", idx)
	}
}

func TestVersionIsPinned(t *testing.T) {
	// ロジック変更時は Version を上げ、全インデックスを再構築すること（仕様書16.3）
	if Version != 1 {
		t.Error("Version を変更した場合、golden.json の更新とインデックス全体の再構築が必要")
	}
}
