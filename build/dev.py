#!/usr/bin/env python3
"""ローカル開発サーバー（フロントエンド用）。

web/src をそのまま配信し、/api/ へのリクエストだけ本番 VPS へ中継する。
ローカルに SQLite も Manticore も置かずに、実データでフロントを開発できる。

  python3 build/dev.py            → http://localhost:8088/
  python3 build/dev.py 9000       → ポート指定

中継しているのは、ブラウザから見て「同一オリジン」にするため。
別オリジンへ直接 fetch すると CORS で弾かれるが、本番に CORS を開けるのは
公開APIを誰でも叩ける状態にすることなので避ける（開発の都合で本番を緩めない）。

標準ライブラリのみ。依存なし。
"""
import http.server
import os
import sys
import urllib.error
import urllib.request

UPSTREAM = os.environ.get("KAKO_UPSTREAM", "https://kakolog.info")
ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "web", "src")


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=ROOT, **kw)

    def end_headers(self):
        # 開発中はブラウザにキャッシュさせない。Last-Modified による再検証だけだと
        # 「編集したのに反映されない」の切り分けが難しくなるため、原因から潰しておく
        self.send_header("Cache-Control", "no-store, must-revalidate")
        super().end_headers()

    def do_GET(self):
        if self.path.startswith("/api/"):
            self.proxy()
            return
        # SPA: 実ファイルが無ければ index.html を返す（?q=... で直接開けるように）
        rel = self.path.split("?", 1)[0].lstrip("/")
        if rel and not os.path.exists(os.path.join(ROOT, rel)):
            self.path = "/index.html"
        super().do_GET()

    def proxy(self):
        url = UPSTREAM + self.path
        req = urllib.request.Request(url, headers={"User-Agent": "kakolog-dev-proxy"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                body, status, ctype = r.read(), r.status, r.headers.get("Content-Type", "")
        except urllib.error.HTTPError as e:
            # 400/503 などは本文（日本語のエラーメッセージ）ごと通す
            body, status, ctype = e.read(), e.code, e.headers.get("Content-Type", "")
        except Exception as e:
            body = ('{"error":{"code":"proxy_error","message":"%s に接続できません: %s"}}'
                    % (UPSTREAM, e)).encode()
            status, ctype = 502, "application/json; charset=utf-8"
        self.send_response(status)
        self.send_header("Content-Type", ctype or "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        sys.stderr.write("  %s\n" % (fmt % args))


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else int(os.environ.get("KAKO_DEV_PORT", 8088))
    print("フロント: %s" % os.path.normpath(ROOT))
    print("API 中継先: %s" % UPSTREAM)
    print()
    print("  → http://localhost:%d/ を開いてください（Ctrl+C で終了）" % port)
    print("  web/src を編集したらブラウザを再読み込みするだけで反映されます")
    print()
    http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n終了しました")
