package store

import (
	"path/filepath"
	"testing"
	"time"
)

// ★ 中核の保証: 5ch 用アンカーと Latest update は独立に書ける。
// 片方だけ更新したときに、もう片方が 0 で潰れてはならない。
func TestScrapeStateAnchorIndependent(t *testing.T) {
	db := testDB(t)
	now := time.Unix(1786200000, 0)
	latest := time.Unix(1786101654, 0)

	// アンカーだけ記録（Latest update が読めない板・bootstrap 時のケース）
	if err := SaveScrapeState(db, "livejupiter", time.Time{}, 1786101654, now); err != nil {
		t.Fatal(err)
	}
	m, _ := LoadScrapeState(db)
	if m["livejupiter"].Anchor5ch != 1786101654 {
		t.Fatalf("anchor_5ch = %d", m["livejupiter"].Anchor5ch)
	}
	if !m["livejupiter"].LatestAt.IsZero() {
		t.Errorf("latest_at が勝手に入っている: %v", m["livejupiter"].LatestAt)
	}

	// Latest update だけ更新してもアンカーは据え置かれること
	if err := SaveScrapeState(db, "livejupiter", latest, 0, now); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadScrapeState(db)
	if m["livejupiter"].Anchor5ch != 1786101654 {
		t.Errorf("anchor_5ch が潰れた: %d", m["livejupiter"].Anchor5ch)
	}
	if !m["livejupiter"].LatestAt.Equal(latest) {
		t.Errorf("latest_at = %v, want %v", m["livejupiter"].LatestAt, latest)
	}

	// アンカーだけ進める
	if err := SaveScrapeState(db, "livejupiter", time.Time{}, 1786999999, now); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadScrapeState(db)
	if m["livejupiter"].Anchor5ch != 1786999999 || !m["livejupiter"].LatestAt.Equal(latest) {
		t.Errorf("状態 = %+v", m["livejupiter"])
	}
}

// anchor_5ch を持たない世代の DB に対して、列が追加されること。
func TestEnsureScrapeStateMigratesOldSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE scrape_state (
		board_id TEXT PRIMARY KEY, latest_at INTEGER NOT NULL, scanned_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO scrape_state VALUES ('news4vip', 1786101654, 1786200000)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureScrapeState(db); err != nil {
		t.Fatalf("マイグレーションに失敗: %v", err)
	}
	m, err := LoadScrapeState(db)
	if err != nil {
		t.Fatal(err)
	}
	if m["news4vip"].Anchor5ch != 0 {
		t.Errorf("旧世代の行は anchor_5ch = 0 であるべき: %+v", m["news4vip"])
	}
	// 2回目の Ensure が落ちないこと（duplicate column を握り潰す）
	if err := EnsureScrapeState(db); err != nil {
		t.Fatalf("2回目の Ensure に失敗: %v", err)
	}
}

func TestSCKakoCounts(t *testing.T) {
	db := testDB(t)
	if err := EnsureSCState(db); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1786200000, 0)
	if err := SaveSCKakoCount(db, "livejupiter", 1787, 214, now); err != nil {
		t.Fatal(err)
	}
	if err := SaveSCKakoCount(db, "livejupiter", 1786, 1284, now); err != nil {
		t.Fatal(err)
	}
	// 同じ倉庫の更新は UPSERT
	if err := SaveSCKakoCount(db, "livejupiter", 1787, 260, now); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSCKakoCounts(db, "livejupiter")
	if err != nil {
		t.Fatal(err)
	}
	if m[1787] != 260 || m[1786] != 1284 || len(m) != 2 {
		t.Errorf("counts = %v", m)
	}
	if other, _ := LoadSCKakoCounts(db, "news4vip"); len(other) != 0 {
		t.Errorf("他板の値が混ざっている: %v", other)
	}
}

func TestSCSourced(t *testing.T) {
	db := testDB(t)
	if err := EnsureSCState(db); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1786200000, 0)
	for _, tk := range []int64{1787000001, 1787000002} {
		if err := MarkSCSourced(db, "livejupiter", tk, now); err != nil {
			t.Fatal(err)
		}
	}
	// 二重記録で落ちないこと
	if err := MarkSCSourced(db, "livejupiter", 1787000001, now); err != nil {
		t.Fatal(err)
	}
	keys, err := SCSourcedKeys(db, "livejupiter")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !keys[1787000001] {
		t.Fatalf("keys = %v", keys)
	}
	n, err := CountSCSourced(db)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, err = %v", n, err)
	}

	// 5ch で裏が取れたら消える
	if err := ClearSCSourced(db, "livejupiter", 1787000001); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountSCSourced(db); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

// 旧デプロイDB（テーブルが無い）でもフッター表示が落ちないこと。
func TestCountSCSourcedWithoutTable(t *testing.T) {
	db := testDB(t)
	n, err := CountSCSourced(db)
	if err != nil || n != 0 {
		t.Fatalf("count = %d, err = %v", n, err)
	}
}
