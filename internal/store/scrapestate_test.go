package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := EnsureScrapeState(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestScrapeStateRoundTrip(t *testing.T) {
	db := testDB(t)
	now := time.Unix(1786200000, 0)
	latest := time.Unix(1786101654, 0)

	// 記録が無い板は map に現れない（＝ゼロ値 → 門番が働かず必ず本スキャンされる）
	m, err := LoadScrapeState(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["livejupiter"]; ok {
		t.Fatal("空の DB から値が返っている")
	}
	if !m["livejupiter"].IsZero() {
		t.Fatal("未記録の板はゼロ値であるべき")
	}

	if err := SaveScrapeState(db, "livejupiter", latest, now); err != nil {
		t.Fatal(err)
	}
	// 2回目は UPSERT で上書きされること（PRIMARY KEY 衝突で落ちない）
	newer := latest.Add(24 * time.Hour)
	if err := SaveScrapeState(db, "livejupiter", newer, now); err != nil {
		t.Fatal(err)
	}
	m, err = LoadScrapeState(db)
	if err != nil {
		t.Fatal(err)
	}
	if !m["livejupiter"].Equal(newer) {
		t.Errorf("latest_at = %v, want %v", m["livejupiter"], newer)
	}
}

func TestScrapeStateIgnoresZeroLatest(t *testing.T) {
	// Latest update の記載が無い板は記録しない。記録すると次回以降ゼロ値と比較され
	// かねないため、そもそも行を作らず毎回本スキャンさせる。
	db := testDB(t)
	if err := SaveScrapeState(db, "oldstyle", time.Time{}, time.Unix(1786200000, 0)); err != nil {
		t.Fatal(err)
	}
	m, err := LoadScrapeState(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("ゼロ値の Latest update が記録されている: %v", m)
	}
}

func TestScrapeStateRollbackKeepsOldValue(t *testing.T) {
	// ★ 中核の保証: 投入トランザクションが失敗したら Latest update も巻き戻り、
	// 次回そのぶんが必ず再試行される（据え置き）。
	db := testDB(t)
	old := time.Unix(1786101654, 0)
	if err := SaveScrapeState(db, "livejupiter", old, time.Unix(1786200000, 0)); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveScrapeState(tx, "livejupiter", old.Add(48*time.Hour), time.Unix(1786300000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	m, err := LoadScrapeState(db)
	if err != nil {
		t.Fatal(err)
	}
	if !m["livejupiter"].Equal(old) {
		t.Errorf("ロールバック後の latest_at = %v, want %v（据え置かれていない）", m["livejupiter"], old)
	}
}
