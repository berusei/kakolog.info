#!/usr/bin/env python3
"""ローカル検証用の簡易検索 CLI（API 実装までのつなぎ）。

    python3 build/localsearch.py チーズケーキ
    python3 build/localsearch.py チーズ --board livejupiter --sort old --limit 20

注意: 正規化は本物（internal/tokenizer）の近似。NFKC+小文字化+1文字分割のみ。
本番の検索経路の検証には使わないこと（それはフェーズ8の API と Go テストの仕事）。
"""
import argparse
import datetime
import json
import sqlite3
import unicodedata
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:19328"
DOC_ID_MAX, MULT = 4_400_000_000_000, 2048
RESERVED = set('\\()|-!@~"&/^$=<')
JST = datetime.timezone(datetime.timedelta(hours=9))


def tokens(word: str):
    s = unicodedata.normalize("NFC", unicodedata.normalize("NFKC", word)).lower()
    return ["".join("\\" + c if c in RESERVED else c for c in ch) for ch in s if not ch.isspace()]


def sql(q):
    data = urllib.parse.urlencode({"query": q}).encode()
    with urllib.request.urlopen(BASE + "/sql?mode=raw", data=data, timeout=15) as r:
        res = json.loads(r.read())[0]
    if res.get("error"):
        raise SystemExit("searchd error: " + res["error"])
    return res["data"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("words", nargs="+", help="検索語（複数指定で AND）")
    ap.add_argument("--board")
    ap.add_argument("--sort", choices=["new", "old"], default="new")
    ap.add_argument("--limit", type=int, default=10)
    a = ap.parse_args()

    phrases = " ".join(f'"{" ".join(tokens(w))}"' for w in a.words)
    match = f"@title ({phrases})"
    if a.board:
        match += f" @board (b_{a.board})"
    table = "kako_new" if a.sort == "new" else "kako_old"
    # MATCH と SHOW META は同一コネクションでなければならない（仕様書11.2）。
    # HTTP では1リクエストに束ねることで満たす。
    data = urllib.parse.urlencode({"query": (
        f"SELECT id FROM {table} WHERE MATCH('{match}') ORDER BY id ASC "
        f"LIMIT {a.limit} OPTION ranker=none, max_matches=1000, cutoff=1000; SHOW META"
    )}).encode()
    with urllib.request.urlopen(BASE + "/sql?mode=raw", data=data, timeout=15) as r:
        res = json.loads(r.read())
    for part in res:
        if part.get("error"):
            raise SystemExit("searchd error: " + part["error"])
    rows = res[0]["data"]
    db = sqlite3.connect("file:db/kakolog.db?mode=ro", uri=True)
    cur = db.cursor()
    idx2board = dict(cur.execute("SELECT board_idx, board_id FROM boards"))
    for r in rows:
        asc = r["id"] if a.sort == "old" else DOC_ID_MAX - r["id"]
        tk, bidx = asc // MULT, asc % MULT
        board = idx2board.get(bidx, "?")
        row = cur.execute(
            "SELECT title, res_count FROM threads WHERE board_id=? AND thread_key=?",
            (board, tk)).fetchone()
        title, res_count = row if row else ("(SQLiteに見つからず)", 0)
        d = datetime.datetime.fromtimestamp(tk, JST).strftime("%Y-%m-%d")
        print(f"{d}  [{board}] {title} ({res_count})")
    meta = {r["Variable_name"]: r["Value"] for r in res[1]["data"]}
    total = meta.get("total_found", "?")
    print(f"-- total_found: {total}" + ("+" if meta.get("total") == "1000" else ""))


if __name__ == "__main__":
    main()
