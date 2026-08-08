// Package docid は doc_id の合成・分解・期間レンジ変換を提供する（仕様書4.1, 11.3）。
//
// ★ board_idx は doc_id に焼き込まれる（仕様書16.2）。一度割り当てた board_idx を
// 変更した瞬間、全インデックスが無効になる。新規板は必ず末尾に追加すること。
package docid

import (
	"fmt"
	"time"
)

const (
	// BoardBits / BoardMult: doc_id の下位11ビットが board_idx（仕様書4.1）。
	BoardBits = 11
	BoardMult = 1 << BoardBits // 2048

	// DocIDMax は kako_new 用の doc_id 反転に使う固定値（仕様書4.1.1）。
	// thread_key 上限 2.1e9 * 2048 を超える値。変更してはならない。
	DocIDMax = 4_400_000_000_000

	// ThreadKeyExcludeMin 以上の thread_key は壊れたデータとして
	// エクスポート時に除外する（G1決定 2026-08-04、docs/data-audit.md 参照）。
	// この閾値が DocIDMax の前提（thread_key < 2.1e9）を守っている。
	ThreadKeyExcludeMin = 2_100_000_000
)

// Asc は kako_old 用 doc_id（昇順 = 時系列昇順）を合成する。
func Asc(threadKey, boardIdx int64) int64 {
	return threadKey*BoardMult + boardIdx
}

// New は kako_new 用 doc_id（昇順 = 時系列降順）を合成する。常に正。
func New(threadKey, boardIdx int64) int64 {
	return DocIDMax - Asc(threadKey, boardIdx)
}

// SplitAsc は kako_old の doc_id を (thread_key, board_idx) に分解する。
func SplitAsc(id int64) (threadKey, boardIdx int64) {
	return id / BoardMult, id % BoardMult
}

// SplitNew は kako_new の doc_id を (thread_key, board_idx) に分解する。
func SplitNew(id int64) (threadKey, boardIdx int64) {
	return SplitAsc(DocIDMax - id)
}

// JST は本システムの固定タイムゾーン（仕様書 付録B）。DST が無いため FixedZone でよい。
var JST = time.FixedZone("JST", 9*3600)

// MonthBounds は "YYYY-MM" を JST の [月初00:00:00, 月末23:59:59] の UNIX 秒に変換する。
func MonthBounds(ym string) (tsFrom, tsTo int64, err error) {
	t, err := time.ParseInLocation("2006-01", ym, JST)
	if err != nil {
		return 0, 0, fmt.Errorf("期間は YYYY-MM 形式で指定してください: %q", ym)
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, JST)
	next := start.AddDate(0, 1, 0)
	return start.Unix(), next.Unix() - 1, nil
}

// DayBounds は "YYYY-MM-DD" を JST の [その日00:00:00, 23:59:59] の UNIX 秒に変換する。
func DayBounds(ymd string) (tsFrom, tsTo int64, err error) {
	t, err := time.ParseInLocation("2006-01-02", ymd, JST)
	if err != nil {
		return 0, 0, fmt.Errorf("日付は YYYY-MM-DD 形式で指定してください: %q", ymd)
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, JST)
	next := start.AddDate(0, 0, 1)
	return start.Unix(), next.Unix() - 1, nil
}

// PeriodBounds は "YYYY-MM"（その月全体）と "YYYY-MM-DD"（その日全体）の両方を受け取り、
// 指定された単位の全体を覆う閉区間を返す（仕様書10.1 の from/to の意味を粒度だけ拡張したもの）。
//
// API の from/to はこれを通す。YYYY-MM を受け付け続けるのは、既存の共有リンクや
// ブックマーク（`?from=2015-01&to=2015-12`）を壊さないため（2026-08-06 の日付指定追加時）。
// 呼び出し側は from に tsFrom、to に tsTo を使う。どちらの形式でも「指定した単位を丸ごと含む」
// という意味は変わらない。
func PeriodBounds(s string) (tsFrom, tsTo int64, err error) {
	if len(s) == len("YYYY-MM-DD") {
		return DayBounds(s)
	}
	from, to, err := MonthBounds(s)
	if err != nil {
		// 形式が判別できないときは、どちらでもよいことが分かる案内にする
		return 0, 0, fmt.Errorf("期間は YYYY-MM または YYYY-MM-DD 形式で指定してください: %q", s)
	}
	return from, to, nil
}

// AscRange は UNIX 秒の閉区間を kako_old の doc_id 閉区間に変換する（仕様書11.3）。
func AscRange(tsFrom, tsTo int64) (lo, hi int64) {
	return tsFrom * BoardMult, tsTo*BoardMult + BoardMult - 1
}

// NewRange は UNIX 秒の閉区間を kako_new の doc_id 閉区間に変換する（仕様書11.3）。
// kako_new では大小が反転することに注意。変換はこの関数だけで行うこと。
func NewRange(tsFrom, tsTo int64) (lo, hi int64) {
	ascLo, ascHi := AscRange(tsFrom, tsTo)
	return DocIDMax - ascHi, DocIDMax - ascLo
}

// CreatedAt は thread_key から作成日時（JST）を導出する（仕様書2.2, 16.5）。
func CreatedAt(threadKey int64) time.Time {
	return time.Unix(threadKey, 0).In(JST)
}
