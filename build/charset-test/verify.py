#!/usr/bin/env python3
"""charset_table 検証（仕様書6.4）。

chars.txt の各トークンを CALL KEYWORDS に1つずつかけ、searchd のトークナイザを
通過して生き残るかを実測する。落ちた文字は charset_table への明示追加が必要。
使い方: python3 verify.py [http://127.0.0.1:19308]
"""
import json
import sys
import urllib.parse
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:19308"


def sql(query: str):
    data = urllib.parse.urlencode({"query": query}).encode()
    with urllib.request.urlopen(BASE + "/sql?mode=raw", data=data, timeout=10) as r:
        return json.loads(r.read())


def sql_escape(s: str) -> str:
    return s.replace("\\", "\\\\").replace("'", "\\'")


# 仕様書5.3: 検索クエリ側で必要な Manticore 予約文字のエスケープ。
# internal/tokenizer.EscapeToken と同一リストであること。
RESERVED = set('\\()|-!@~"&/^$=<')


def query_escape(s: str) -> str:
    return "".join(("\\" + c) if c in RESERVED else c for c in s)


def keywords(token: str):
    res = sql(f"CALL KEYWORDS('{sql_escape(query_escape(token))}', 'kako_test')")
    rows = res[0]["data"] if isinstance(res, list) else res["data"]
    return [(r["tokenized"], r["normalized"]) for r in rows]


def main():
    chars = [l.rstrip("\n") for l in open("chars.txt", encoding="utf-8") if l.strip()]
    ok, dropped, changed = [], [], []
    for tok in chars:
        rows = keywords(tok)
        if not rows:
            dropped.append(tok)
        elif len(rows) == 1 and rows[0][1] == tok:
            ok.append(tok)
        else:
            changed.append((tok, rows))

    print(f"生存: {len(ok)} / 消滅: {len(dropped)} / 変形: {len(changed)}")
    if dropped:
        print("\n--- 消滅（charset_table に追加が必要）---")
        for t in dropped:
            cps = " ".join(f"U+{ord(c):04X}" for c in t)
            print(f"  {t!r}  [{cps}]")
    if changed:
        print("\n--- 変形（トークン数または表記が変化）---")
        for t, rows in changed:
            cps = " ".join(f"U+{ord(c):04X}" for c in t)
            print(f"  {t!r} [{cps}] -> {rows}")
    sys.exit(1 if dropped or changed else 0)


if __name__ == "__main__":
    main()
