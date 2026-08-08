#!/usr/bin/env python3
"""フェーズ6 ベンチマーク（仕様書12.5）。VPS 上で実行する:

    python3 bench.py

各クエリ形状を12回ずつ実行し、初回（コールド）と2回目以降の p50/p95 を出す。
searchd は 127.0.0.1:9308 (HTTP, ループバック限定) で稼働していること。
"""
import json
import statistics
import time
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:9308"
DOC_ID_MAX, MULT = 4_400_000_000_000, 2048
OPT = "OPTION ranker=none, max_matches=1000, cutoff=1000"

# 2025-08-01 〜 2026-07-31 JST の doc_id 範囲（kako_new 用）
TS_FROM, TS_TO = 1753974000, 1785509999
NLO = DOC_ID_MAX - (TS_TO * MULT + MULT - 1)
NHI = DOC_ID_MAX - TS_FROM * MULT

CASES = [
    ("2文字+板指定 (目標p95 200ms)", [
        f"SELECT id FROM kako_new WHERE MATCH('@title \"チ ー ズ\" @board b_livejupiter') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"野 球\" @board b_news4vip') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"結 婚\" @board b_poverty') ORDER BY id ASC LIMIT 50 {OPT}",
    ]),
    ("2文字以上+全板全期間 (目標p95 500ms)", [
        f"SELECT id FROM kako_new WHERE MATCH('@title \"チ ー ズ ケ ー キ\"') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"ド ラ ゴ ン\"') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"ラ ー メ ン\"') ORDER BY id ASC LIMIT 50 {OPT}",
    ]),
    ("1文字+板指定 (目標p95 500ms)", [
        f"SELECT id FROM kako_new WHERE MATCH('@title \"の\" @board b_morningcoffee') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"猫\" @board b_livejupiter') ORDER BY id ASC LIMIT 50 {OPT}",
    ]),
    ("1文字+期間1年 (12.2の代替絞り込み)", [
        f"SELECT id FROM kako_new WHERE MATCH('@title \"の\"') AND id BETWEEN {NLO} AND {NHI} ORDER BY id ASC LIMIT 50 {OPT}",
    ]),
    ("深い20ページ目 (目標p95 1s)", [
        f"SELECT id FROM kako_new WHERE MATCH('@title \"ラ ー メ ン\"') ORDER BY id ASC LIMIT 950, 50 {OPT}",
        f"SELECT id FROM kako_new WHERE MATCH('@title \"の\" @board b_livejupiter') ORDER BY id ASC LIMIT 950, 50 {OPT}",
    ]),
    ("kako_old 側の代表 (古い順)", [
        f"SELECT id FROM kako_old WHERE MATCH('@title \"ラ ー メ ン\"') ORDER BY id ASC LIMIT 50 {OPT}",
        f"SELECT id FROM kako_old WHERE MATCH('@title \"の\" @board b_news4vip') ORDER BY id ASC LIMIT 50 {OPT}",
    ]),
]

REPEAT = 12


def sql(q):
    data = urllib.parse.urlencode({"query": q}).encode()
    with urllib.request.urlopen(BASE + "/sql?mode=raw", data=data, timeout=15) as r:
        res = json.loads(r.read())[0]
    if res.get("error"):
        raise RuntimeError(res["error"][:200])
    return len(res["data"])


def main():
    print("== kakosearch ベンチマーク ==")
    total = sql("SELECT COUNT(*) FROM kako_new")
    for label, queries in CASES:
        cold, warm = [], []
        for q in queries:
            for i in range(REPEAT):
                t0 = time.time()
                try:
                    n = sql(q)
                except Exception as e:
                    print(f"  ERROR: {e}")
                    break
                ms = (time.time() - t0) * 1000
                (cold if i == 0 else warm).append(ms)
        if not warm:
            continue
        w = sorted(warm)
        p50 = w[len(w) // 2]
        p95 = w[min(len(w) - 1, int(len(w) * 0.95))]
        print(f"[{label}]")
        print(f"  初回(コールド): {' / '.join(f'{c:.0f}ms' for c in cold)}")
        print(f"  2回目以降: p50 {p50:.0f}ms / p95 {p95:.0f}ms / min {w[0]:.0f}ms / max {w[-1]:.0f}ms")


if __name__ == "__main__":
    main()
