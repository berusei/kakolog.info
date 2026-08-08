package docid

import (
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	cases := []struct{ tk, bi int64 }{
		{924070201, 0},           // データ中の最小 thread_key
		{1689063651, 901},        // 中間値・最大 board_idx 近傍
		{ThreadKeyExcludeMin - 1, BoardMult - 1}, // 設計上の上限
		{0, 0},
	}
	for _, c := range cases {
		asc := Asc(c.tk, c.bi)
		if tk, bi := SplitAsc(asc); tk != c.tk || bi != c.bi {
			t.Errorf("SplitAsc(Asc(%d,%d)) = (%d,%d)", c.tk, c.bi, tk, bi)
		}
		nw := New(c.tk, c.bi)
		if nw <= 0 {
			t.Errorf("New(%d,%d) = %d, 正でなければならない", c.tk, c.bi, nw)
		}
		if tk, bi := SplitNew(nw); tk != c.tk || bi != c.bi {
			t.Errorf("SplitNew(New(%d,%d)) = (%d,%d)", c.tk, c.bi, tk, bi)
		}
	}
}

func TestNewIsReversed(t *testing.T) {
	// thread_key が新しいほど kako_new の doc_id は小さい
	// → ORDER BY id ASC が新しい順になる（仕様書4.1.1）
	older := New(1600000000, 100)
	newer := New(1700000000, 100)
	if newer >= older {
		t.Error("新しいスレの kako_new doc_id は小さくなるべき")
	}
}

func TestMonthBounds(t *testing.T) {
	cases := []struct {
		ym       string
		from, to int64
		days     int64
	}{
		// 2023-01-01 00:00:00 JST = 2022-12-31T15:00:00Z
		{"2023-01", 1672498800, 1675177199, 31},
		{"2024-02", 0, 0, 29}, // うるう年
		{"2023-02", 0, 0, 28},
		{"2023-12", 0, 0, 31}, // 年跨ぎ（翌月が2024-01）
	}
	for _, c := range cases {
		from, to, err := MonthBounds(c.ym)
		if err != nil {
			t.Fatalf("MonthBounds(%q): %v", c.ym, err)
		}
		if c.from != 0 && (from != c.from || to != c.to) {
			t.Errorf("MonthBounds(%q) = (%d,%d), want (%d,%d)", c.ym, from, to, c.from, c.to)
		}
		if got := (to - from + 1) / 86400; got != c.days {
			t.Errorf("MonthBounds(%q) の日数 = %d, want %d", c.ym, got, c.days)
		}
		// 翌月の月初と連続していること（隙間も重複もない）
		nextStart := time.Unix(to+1, 0).In(JST)
		if nextStart.Day() != 1 || nextStart.Hour() != 0 {
			t.Errorf("MonthBounds(%q) の to+1 は翌月1日00:00であるべき: %v", c.ym, nextStart)
		}
	}
}

func TestMonthBoundsInvalid(t *testing.T) {
	for _, ym := range []string{"2023-13", "2023-00", "2023-1", "202301", "abc", ""} {
		if _, _, err := MonthBounds(ym); err == nil {
			t.Errorf("MonthBounds(%q) はエラーを返すべき", ym)
		}
	}
}

// 日付単位の指定（2026-08-06 追加）。年→月→日の絞り込みで使う。
func TestDayBounds(t *testing.T) {
	from, to, err := DayBounds("2023-01-01")
	if err != nil {
		t.Fatal(err)
	}
	// 月初の日は、その月の from と一致する
	mFrom, _, _ := MonthBounds("2023-01")
	if from != mFrom {
		t.Errorf("2023-01-01 の from = %d, want %d（月初と一致すべき）", from, mFrom)
	}
	if to-from+1 != 86400 {
		t.Errorf("1日の長さ = %d秒, want 86400", to-from+1)
	}
	// 月末の日は、その月の to と一致する（境界に隙間も重複もない）
	_, mTo, _ := MonthBounds("2023-01")
	_, dTo, _ := DayBounds("2023-01-31")
	if dTo != mTo {
		t.Errorf("2023-01-31 の to = %d, want %d（月末と一致すべき）", dTo, mTo)
	}
	// うるう日
	if _, _, err := DayBounds("2024-02-29"); err != nil {
		t.Errorf("2024-02-29 は有効な日付: %v", err)
	}
	// 存在しない日付は弾く（Go の ParseInLocation は 2023-02-30 をエラーにする）
	for _, s := range []string{"2023-02-30", "2023-13-01", "2023-01-32", "2023-1-1", "20230101", ""} {
		if _, _, err := DayBounds(s); err == nil {
			t.Errorf("DayBounds(%q) はエラーを返すべき", s)
		}
	}
}

// PeriodBounds は YYYY-MM と YYYY-MM-DD の両方を受ける。
// ★ YYYY-MM を受け付け続けるのは既存の共有リンクを壊さないため。この互換は外さないこと。
func TestPeriodBounds(t *testing.T) {
	mFrom, mTo, _ := MonthBounds("2023-07")
	pFrom, pTo, err := PeriodBounds("2023-07")
	if err != nil || pFrom != mFrom || pTo != mTo {
		t.Errorf("PeriodBounds(\"2023-07\") = (%d,%d,%v), want (%d,%d,nil)", pFrom, pTo, err, mFrom, mTo)
	}
	dFrom, dTo, _ := DayBounds("2023-07-15")
	pFrom, pTo, err = PeriodBounds("2023-07-15")
	if err != nil || pFrom != dFrom || pTo != dTo {
		t.Errorf("PeriodBounds(\"2023-07-15\") = (%d,%d,%v), want (%d,%d,nil)", pFrom, pTo, err, dFrom, dTo)
	}
	for _, s := range []string{"2023-13", "2023-02-30", "abc", "", "2023-07-15-01"} {
		if _, _, err := PeriodBounds(s); err == nil {
			t.Errorf("PeriodBounds(%q) はエラーを返すべき", s)
		}
	}
}

func TestRanges(t *testing.T) {
	from, to, _ := MonthBounds("2023-07")

	lo, hi := AscRange(from, to)
	if Asc(from, 0) != lo {
		t.Error("AscRange.lo は月初・board_idx=0 の doc_id と一致すべき")
	}
	if Asc(to, BoardMult-1) != hi {
		t.Error("AscRange.hi は月末秒・board_idx=2047 の doc_id と一致すべき")
	}
	// 区間の外側は含まれない
	if Asc(from-1, BoardMult-1) >= lo {
		t.Error("月初の1秒前が区間に入っている")
	}
	if Asc(to+1, 0) <= hi {
		t.Error("翌月の1秒目が区間に入っている")
	}

	nlo, nhi := NewRange(from, to)
	if New(to, BoardMult-1) != nlo || New(from, 0) != nhi {
		t.Errorf("NewRange の境界が反転変換と一致しない: (%d,%d)", nlo, nhi)
	}
	// 期間内の任意の doc_id が区間に収まる
	mid := New((from+to)/2, 1000)
	if mid < nlo || mid > nhi {
		t.Error("期間中央の doc_id が NewRange に入らない")
	}
}

func TestCreatedAt(t *testing.T) {
	// 期待値は TZ=Asia/Tokyo date -d @1689063651 で裏取り済み
	// （仕様書10.1の例示 18:40:51 は誤りで、正しくは 17:20:51）
	got := CreatedAt(1689063651).Format("2006-01-02T15:04:05-07:00")
	if got != "2023-07-11T17:20:51+09:00" {
		t.Errorf("CreatedAt(1689063651) = %s, want 2023-07-11T17:20:51+09:00", got)
	}
}
