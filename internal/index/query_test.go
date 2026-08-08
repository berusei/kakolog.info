package index

import (
	"fmt"
	"strings"
	"testing"

	"kakosearch/internal/docid"
)

func TestParseQuery(t *testing.T) {
	words := ParseQuery(`チーズ -ケーキ "完全一致" 　全角区切り`)
	want := []Word{
		{"チーズ", false},
		{"ケーキ", true},
		{"完全一致", false},
		{"全角区切り", false},
	}
	if len(words) != len(want) {
		t.Fatalf("ParseQuery = %v", words)
	}
	for i := range want {
		if words[i] != want[i] {
			t.Errorf("words[%d] = %v, want %v", i, words[i], want[i])
		}
	}
	// "-" 単体は除外語にならず落ちる
	if w := ParseQuery("- チーズ"); len(w) != 2 || w[0].Text != "-" || w[0].Exclude {
		// 先頭 - のみの語は len>1 条件を満たさないため通常語 "-" として残る
		t.Errorf("ParseQuery(- チーズ) = %v", w)
	}
}

func TestPositiveTokenCount(t *testing.T) {
	cases := []struct {
		q    string
		want int
	}{
		{"の", 1},
		{"チーズ", 3},
		{"の -あ", 1}, // 除外語は数えない
		{"の は", 2},  // 合計2トークンなら1文字扱いにならない
		{"チーズ ケーキ", 6},
	}
	for _, c := range cases {
		if got := PositiveTokenCount(ParseQuery(c.q)); got != c.want {
			t.Errorf("PositiveTokenCount(%q) = %d, want %d", c.q, got, c.want)
		}
	}
}

func TestBuildMatch(t *testing.T) {
	expr, ok := BuildMatch(ParseQuery("チーズ -ケーキ"), []string{"livejupiter"})
	if !ok {
		t.Fatal("ok = false")
	}
	want := `@title ("チ ー ズ" -"ケ ー キ") @board (b_livejupiter)`
	if expr != want {
		t.Errorf("BuildMatch = %q, want %q", expr, want)
	}

	// 予約文字がエスケープされる
	expr, _ = BuildMatch(ParseQuery("(笑)"), nil)
	if !strings.Contains(expr, `\( 笑 \)`) {
		t.Errorf("予約文字が未エスケープ: %q", expr)
	}

	// 除外語のみ → ok=false
	if _, ok := BuildMatch(ParseQuery("-ケーキ"), nil); ok {
		t.Error("除外語のみで ok=true になっている")
	}
	// 正規化で空になる語のみ → ok=false
	if _, ok := BuildMatch(ParseQuery("�"), nil); ok {
		t.Error("空トークン語のみで ok=true になっている")
	}
}

func TestBuildBrowseMatch(t *testing.T) {
	expr, ok := BuildBrowseMatch([]string{"livejupiter"})
	if !ok || expr != "@board (b_livejupiter)" {
		t.Errorf("BuildBrowseMatch = %q, %v", expr, ok)
	}
	// 複数板は OR
	expr, _ = BuildBrowseMatch([]string{"livejupiter", "119"})
	if expr != "@board (b_livejupiter | b_119)" {
		t.Errorf("複数板 = %q", expr)
	}
	// 板なしは不可。全インデックス走査になり、仕様書1.2 の懸念がそのまま現実になる
	if _, ok := BuildBrowseMatch(nil); ok {
		t.Error("板なしで ok=true になっている")
	}
}

func TestRequestSQL(t *testing.T) {
	from, to, _ := docid.MonthBounds("2023-07")

	// kako_new: 反転レンジ
	r := Request{Match: "@title (\"あ\")", SortOld: false, TsFrom: from, TsTo: to, Page: 2, Per: 50}
	q, args := r.SQL()
	if !strings.Contains(q, "FROM kako_new") || !strings.Contains(q, "LIMIT 50, 50") {
		t.Errorf("SQL = %q", q)
	}
	if !strings.Contains(q, "ORDER BY id ASC") {
		t.Error("ORDER BY id ASC でない（反転は禁止。仕様書4.1.1）")
	}
	if !strings.Contains(q, "ranker=none") || !strings.Contains(q, "cutoff=1000") {
		t.Error("OPTION が欠けている")
	}
	// Exact 時は cutoff が「窓+1」まで緩む（依頼者指示による12.1逸脱。max_matches はそのまま）。
	// +1 があることで「窓ちょうど」と「窓超過」を区別でき、超過を 400 で跳ね返せる
	re := r
	re.Exact = true
	qe, _ := re.SQL()
	if !strings.Contains(qe, fmt.Sprintf("cutoff=%d", MaxResultWindow+1)) ||
		!strings.Contains(qe, "max_matches=1000") {
		t.Errorf("Exact の SQL が不正: %q", qe)
	}
	nlo, nhi := docid.NewRange(from, to)
	if args[1] != nlo || args[2] != nhi {
		t.Errorf("kako_new のレンジが不正: %v, want [%d %d]", args[1:], nlo, nhi)
	}

	// kako_old: 素直なレンジ
	r.SortOld = true
	q, args = r.SQL()
	alo, ahi := docid.AscRange(from, to)
	if !strings.Contains(q, "FROM kako_old") || args[1] != alo || args[2] != ahi {
		t.Errorf("kako_old のレンジが不正: %q %v", q, args[1:])
	}

	// 期間未指定 → 全域（0 〜 上限）
	r2 := Request{Match: "x", Page: 1, Per: 50}
	_, args = r2.SQL()
	lo, hi := docid.NewRange(0, docid.ThreadKeyExcludeMin-1)
	if args[1] != lo || args[2] != hi {
		t.Errorf("全域レンジが不正: %v", args[1:])
	}
}

func TestDecomposeRoundTrip(t *testing.T) {
	tk, bi := int64(1689063651), int64(899)
	rNew := Request{SortOld: false}
	if a, b := rNew.Decompose(docid.New(tk, bi)); a != tk || b != bi {
		t.Errorf("new decompose = (%d,%d)", a, b)
	}
	rOld := Request{SortOld: true}
	if a, b := rOld.Decompose(docid.Asc(tk, bi)); a != tk || b != bi {
		t.Errorf("old decompose = (%d,%d)", a, b)
	}
}

// ExactLimit の挙動（VPS実測に基づく性能オプション。既定0=無制限）
func TestEffectiveCutoff(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want int
	}{
		{"非Exact は仕様書どおり cutoff=1000", Request{Page: 1, Per: 50}, Cutoff},
		{"Exact の既定は 窓+1（超過を検出するため）", Request{Exact: true, Page: 1, Per: 50}, MaxResultWindow + 1},
		{"明示指定があればそれを使う", Request{Exact: true, ExactLimit: 100_000, Page: 1, Per: 50}, 100_000},
		{"上限より深いページは必要分まで数える", Request{Exact: true, ExactLimit: 1000, Page: 100, Per: 50}, 5000},
		{"上限内のページは上限のまま", Request{Exact: true, ExactLimit: 100_000, Page: 10, Per: 50}, 100_000},
	}
	for _, c := range cases {
		if got := c.req.effectiveCutoff(); got != c.want {
			t.Errorf("%s: cutoff = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSQLUsesEffectiveCutoff(t *testing.T) {
	q, _ := Request{Match: "x", Exact: true, ExactLimit: 100_000, Page: 1, Per: 50}.SQL()
	if !strings.Contains(q, "cutoff=100000") {
		t.Errorf("cutoff が反映されていない: %s", q)
	}
	q, _ = Request{Match: "x", Exact: true, Page: 1, Per: 50}.SQL()
	if !strings.Contains(q, fmt.Sprintf("cutoff=%d", MaxResultWindow+1)) {
		t.Errorf("既定は 窓+1 であるべき: %s", q)
	}
}
