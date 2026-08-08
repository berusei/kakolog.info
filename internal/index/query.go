// Package index は Manticore への接続とクエリ組み立て（仕様書11章）。
package index

import (
	"fmt"
	"strings"

	"kakosearch/internal/docid"
	"kakosearch/internal/tokenizer"
)

// 性能ガードの定数（仕様書12.1 付録B）。
// Exact モード（依頼者指示による逸脱、2026-08-05）では Cutoff/MaxPage の代わりに
// MaxResultWindow が上限になる。KAKO_EXACT_COUNT=false で仕様書どおりに戻る。
const (
	Cutoff     = 1000
	MaxMatches = 1000
	PerPage    = 50
	MaxPer     = 100 // Exact モード導入に合わせ 50→100（依頼者指示 2026-08-05）
	MaxPage    = 20  // 非 Exact モードの上限（仕様書12.1どおり）
	// MaxResultWindow: Exact モードで扱える結果の総数。【依頼者決定 2026-08-05: 25万件】
	//
	// この値は2つの意味を同時に持つ。
	//   1. ヒット数がこれ以下なら、正確な件数を出し、最後のページまで辿れる
	//   2. これを超えたら結果を返さず 400 too_many_results で絞り込みを促す
	//
	// cutoff をこの値+1 に置くため、超過クエリは**数え切らずに即座に打ち切られる**。
	// 上限を設けること自体が性能ガードとして働く（仕様書12.1の精神と同じ）。
	// VPS実測（2026-08-05、最悪ケース「の」=5,722万ヒット）:
	//   cutoff=  1,000 … 0.195s ／ cutoff=100,000 … 0.182s ← 打ち切りはほぼ無料
	//   cutoff=      0（無制限）… 1.397s
	//   保持件数 100万 … 1.97s ／ 200万 … 2.63s ／ 500万 … 5.44s（3秒超過）
	// 25万はこの安全域に十分収まり、かつ実用的な検索（ラブライブ18万件など）を
	// 最後まで辿れる水準として選んだ。
	MaxResultWindow = 250_000
)

// Word は検索語1つ。Exclude は先頭 `-` の除外指定。
type Word struct {
	Text    string
	Exclude bool
}

// ParseQuery は q を空白（半角・全角）で分割し、除外指定を判定する（仕様書11.2 手順1）。
func ParseQuery(q string) []Word {
	var words []Word
	for _, f := range strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '　'
	}) {
		w := Word{Text: f}
		if strings.HasPrefix(f, "-") && len(f) > 1 {
			w.Exclude = true
			w.Text = f[1:]
		}
		// 初版では引用符は通常の部分一致と同じ扱い（仕様書11.1）。予約文字として
		// エスケープされるため、ここでは剥がすだけにする
		w.Text = strings.Trim(w.Text, `"「」`)
		if w.Text == "" {
			continue
		}
		words = append(words, w)
	}
	return words
}

// PositiveTokenCount は肯定語の合計トークン数。1文字クエリ判定（仕様書12.2）に使う。
func PositiveTokenCount(words []Word) int {
	n := 0
	for _, w := range words {
		if !w.Exclude {
			n += len(tokenizer.Tokens(w.Text))
		}
	}
	return n
}

// BuildMatch は MATCH() の中身を組み立てる（仕様書11.2 手順2〜6）。
// 肯定語が1つもない、または全語が正規化で空になった場合は ok=false。
func BuildMatch(words []Word, boards []string) (expr string, ok bool) {
	var parts []string
	positive := false
	for _, w := range words {
		toks := tokenizer.QueryTokens(w.Text) // 正規化+分割+エスケープ（仕様書5.3）
		if len(toks) == 0 {
			continue
		}
		phrase := `"` + strings.Join(toks, " ") + `"`
		if w.Exclude {
			phrase = "-" + phrase
		} else {
			positive = true
		}
		parts = append(parts, phrase)
	}
	if !positive {
		return "", false
	}
	expr = "@title (" + strings.Join(parts, " ") + ")"
	if b := boardExpr(boards); b != "" {
		expr += " " + b
	}
	return expr, true
}

// BuildBrowseMatch は検索語なしの一覧（板＋期間の両方が指定されている場合に限る）の
// MATCH 式を組み立てる。
//
// 【仕様書1.2「検索語なしの全件一覧は作らない」からの逸脱・依頼者指示 2026-08-06】
// 1.3億件のページネーションは無意味かつ危険、という 1.2 の理由はそのまま正しい。
// ここで開放するのは板と期間の両方で絞られた場合だけであり、さらに Exact モードの
// 窓（MaxResultWindow=25万件）を超えるものは呼び出し側が 400 で跳ね返す。
// つまり「全件一覧」ではなく「25万件以下に絞り込まれた一覧」に限定されている。
//
// @title を伴わない @board のみの式になるが、板トークンも通常の転置インデックスの
// 語であり、doclist を doc_id 順に走査して早期終了する点は通常の検索と変わらない。
// 期間指定が id BETWEEN に落ちるため、走査範囲もその年に閉じる（仕様書4.1）。
func BuildBrowseMatch(boards []string) (expr string, ok bool) {
	// 板の指定は必須。ここが空だと全インデックスの走査になり、1.2 の懸念がそのまま現実になる
	if len(boards) == 0 {
		return "", false
	}
	return boardExpr(boards), true
}

// boardExpr は @board 句。板IDには `119` `4649` のような数値のみのものがあるため、
// 必ず `b_` プレフィックスを付ける（仕様書7.1）。
func boardExpr(boards []string) string {
	if len(boards) == 0 {
		return ""
	}
	bs := make([]string, len(boards))
	for i, b := range boards {
		bs[i] = "b_" + b
	}
	return "@board (" + strings.Join(bs, " | ") + ")"
}

// Request は検索1回分の入力（バリデーション済み）。
type Request struct {
	Match   string
	SortOld bool  // true なら kako_old を引く
	TsFrom  int64 // 0 = 下限なし
	TsTo    int64 // 0 = 上限なし
	Page    int   // 1始まり
	Per     int
	// Exact: true なら cutoff を MaxResultWindow+1 まで緩め、その範囲内では
	// total_found を正確な値にする。【仕様書12.1からの逸脱・依頼者指示 2026-08-05】
	// 窓を超えたヒット数は数え切らずに打ち切り、呼び出し側が 400 で跳ね返す。
	// KAKO_EXACT_COUNT=false で仕様書どおりの cutoff=1000・20ページに戻る。
	Exact bool
	// ExactLimit: 数える上限の明示指定（0 = MaxResultWindow+1 を使う）。
	// 環境変数 KAKO_EXACT_COUNT_LIMIT による調整用。通常は指定しない。
	ExactLimit int
}

// SQL は実行する SELECT 文を返す。
//
// ★ 両インデックスとも ORDER BY id ASC（仕様書4.1.1）。ソート方向の切り替えは
// インデックスの選択で行い、ORDER BY を反転させてはならない（早期終了が無効になる）。
// ★ ranker=none は必須（仕様書11.2）。
func (r Request) SQL() (query string, args []any) {
	table := "kako_new"
	if r.SortOld {
		table = "kako_old"
	}
	tsFrom, tsTo := r.TsFrom, r.TsTo
	if tsFrom < 0 {
		tsFrom = 0
	}
	if tsTo <= 0 || tsTo >= docid.ThreadKeyExcludeMin {
		tsTo = docid.ThreadKeyExcludeMin - 1
	}
	// 期間 → doc_id 範囲の変換は docid パッケージに一元化（仕様書11.3）。
	// kako_new では大小が反転することに注意。
	var lo, hi int64
	if r.SortOld {
		lo, hi = docid.AscRange(tsFrom, tsTo)
	} else {
		lo, hi = docid.NewRange(tsFrom, tsTo)
	}
	offset := (r.Page - 1) * r.Per
	cutoff := r.effectiveCutoff()
	mm := MaxMatches
	if r.Exact && mm < offset+r.Per {
		mm = offset + r.Per // 深いページに必要なぶんだけ広げる（上限は呼び出し側で検証）
	}
	query = fmt.Sprintf(
		"SELECT id FROM %s WHERE MATCH(?) AND id BETWEEN ? AND ? "+
			"ORDER BY id ASC LIMIT %d, %d "+
			"OPTION max_matches=%d, cutoff=%d, ranker=none",
		table, offset, r.Per, mm, cutoff)
	return query, []any{r.Match, lo, hi}
}

// effectiveCutoff は実際に発行する cutoff の値を返す（0 = 打ち切りなし）。
// SQL 組み立てと「件数が概算かどうか」の判定が同じ値を見るよう1箇所に閉じ込める。
func (r Request) effectiveCutoff() int {
	if !r.Exact {
		return Cutoff
	}
	limit := r.ExactLimit
	if limit <= 0 {
		// 窓+1 まで数える。「+1」があることで「窓ちょうど」と「窓超過」を区別でき、
		// 超過なら呼び出し側が結果を返さずに絞り込みを促せる
		limit = MaxResultWindow + 1
	}
	// 表示しようとしているページ位置までは必ず数え切る
	if need := (r.Page-1)*r.Per + r.Per; limit < need {
		return need
	}
	return limit
}

// Decompose は結果の doc_id を (thread_key, board_idx) に戻す。
func (r Request) Decompose(id int64) (threadKey, boardIdx int64) {
	if r.SortOld {
		return docid.SplitAsc(id)
	}
	return docid.SplitNew(id)
}
