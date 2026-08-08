// Package config は環境変数からの設定読み込み（仕様書2.0, 16.1）。
package config

import (
	"os"
	"strconv"
)

type Config struct {
	// KAKO_DB_PATH: SQLite のパス。本番は読み取り専用で開く（仕様書8.2）
	DBPath string
	// KAKO_MANTICORE_ADDR: searchd の MySQL プロトコル待ち受け（仕様書10）
	ManticoreAddr string
	// KAKO_LISTEN: API の待ち受けアドレス
	Listen string
	// KAKO_5CH_BASE: 外部リンクのベースドメイン。
	// ★ドメイン移転の実例があるため、コードに直書き禁止（仕様書16.1 MUST）。
	// 再移転時はこの環境変数の変更と再起動のみで復旧できること。
	FiveChBase string
	// KAKO_HAS_OLD: kako_old インデックスが存在するか。
	// false の場合 sort=old は期間指定必須の縮退運用になる（仕様書12.3）
	HasOld bool
	// KAKO_OLD_MAX_RANGE_YEARS: 縮退運用時の期間幅上限（年）
	OldMaxRangeYears int
	// KAKO_STATIC_DIR: 指定時のみ web/dist を配信（ローカル開発用）。
	// 本番は nginx が配信するため未設定にする（仕様書13.3, 17.3）。
	// バイナリへの embed は行わない（17.3 MUST NOT）— ディスクから読むだけ。
	StaticDir string
	// KAKO_EXACT_COUNT: true なら total を正確な件数にする（cutoff なし）。
	// 依頼者指示による仕様書12.1からの逸脱。性能悪化時は false に戻す。
	ExactCount bool
	// KAKO_EXACT_COUNT_LIMIT: 正確に数える上限件数。0（既定）= 無制限。
	// 100000 を指定すると、10万件までは正確・それ以上は「約N件」表示になる代わりに、
	// 最悪ケース（1文字+板指定＝2,358万ヒット）が 1.40s → 0.18s になる（VPS実測）。
	// ページネーションの窓自体が10万件なので、超過分の件数に操作上の意味はない。
	ExactCountLimit int
	// KAKO_BOARD_URLS: 板ごとの過去ログサーバー対応表 JSON
	// （[{"board_id":"...","url":"https://eagle.5ch.io/..."}]）。
	// 未指定または載っていない板は FiveChBase を使う。
	// ドメイン再移転時はこの JSON の差し替え + 再起動で復旧する（仕様書16.1）。
	BoardURLs string
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Load() Config {
	hasOld := true
	if v := os.Getenv("KAKO_HAS_OLD"); v != "" {
		hasOld, _ = strconv.ParseBool(v)
	}
	exact := true
	if v := os.Getenv("KAKO_EXACT_COUNT"); v != "" {
		exact, _ = strconv.ParseBool(v)
	}
	exactLimit := 0
	if v := os.Getenv("KAKO_EXACT_COUNT_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			exactLimit = n
		}
	}
	years := 2
	if v := os.Getenv("KAKO_OLD_MAX_RANGE_YEARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			years = n
		}
	}
	return Config{
		DBPath:           get("KAKO_DB_PATH", "./db/kakolog.db"),
		ManticoreAddr:    get("KAKO_MANTICORE_ADDR", "127.0.0.1:9306"),
		Listen:           get("KAKO_LISTEN", "127.0.0.1:8080"),
		FiveChBase:       get("KAKO_5CH_BASE", "https://kako.5ch.io"),
		HasOld:           hasOld,
		OldMaxRangeYears: years,
		StaticDir:        os.Getenv("KAKO_STATIC_DIR"),
		ExactCount:       exact,
		ExactCountLimit:  exactLimit,
		BoardURLs:        get("KAKO_BOARD_URLS", "./db/board-urls.json"),
	}
}
