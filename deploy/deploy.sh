#!/bin/bash
# アプリ更新のデプロイ（手元PC=WSL 側から実行）。
# バイナリ・フロントエンド・設定データを VPS へ同期し、API を再起動する。
#
# このスクリプトが面倒を見る範囲:
#   - Go バイナリ（kakoapi / kakoctl）
#   - フロントエンド（web/src → web/dist → VPS）
#   - db/boards.json, db/board-urls.json
# 見ない範囲（変更したら手で配置する。docs/DEPLOY.md 参照）:
#   - nginx 設定 ★certbot の追記があるため、取り戻さずに転送すると HTTPS が消える
#   - systemd ユニット / api.env / manticore.conf / rebuild.sh
#   - 索引の再構築（rebuild.sh の役目）
set -euo pipefail

HOST=kakovps
REPO="$(cd "$(dirname "$0")/.." && pwd)"
export PATH=$PATH:$HOME/.local/go/bin
cd "$REPO"

# バックエンドはローカルで動かせない（2026-08-05 に db/ と索引を削除したため）。
# テストが唯一の安全網なので、通らないものは本番に出さない。
# どうしても急ぐときだけ SKIP_TESTS=1 ./deploy/deploy.sh
if [ "${SKIP_TESTS:-0}" != "1" ]; then
  echo "[1/5] テスト..."
  go test ./... >/dev/null || { echo "  ★テストが落ちています。デプロイを中止しました"; exit 1; }
  # フロントの実行時エラーは go test では捕まらない。「API は正常・JS は構文的に正しい・
  # でも描画に到達しない」を止めるための jsdom スモークテスト（2026-08-06 の事故の再発防止）。
  # jsdom 未インストールの環境では警告のみ（終了コード2）でデプロイは続行する。
  if [ -d node_modules/jsdom ]; then
    node web/test/smoke.js || { echo "  ★フロントのスモークテストが落ちています。デプロイを中止しました"; exit 1; }
  else
    echo "  ※ jsdom 未インストールのためフロントのスモークテストを省略（npm install で有効化）"
  fi
  echo "  OK"
fi

echo "[2/5] クロスコンパイル..."
CGO_ENABLED=0 go build -o bin/kakoapi-linux ./cmd/kakoapi
CGO_ENABLED=0 go build -o bin/kakoctl-linux ./cmd/kakoctl

echo "[3/5] フロントエンドを dist へ..."
cp web/src/* web/dist/

echo "[4/5] 転送..."
rsync -av bin/kakoapi-linux "$HOST:/opt/kakosearch/bin/kakoapi.new"
rsync -av bin/kakoctl-linux "$HOST:/opt/kakosearch/bin/kakoctl"
# board-years.json は VPS 側で rebuild.sh が生成する（手元に SQLite が無いため作れない）。
# --delete の対象から外さないと、デプロイのたびに消えて一覧の事前判定が効かなくなる。
rsync -av --delete --exclude=board-years.json web/dist/ "$HOST:/opt/kakosearch/web/"
rsync -av db/boards.json db/board-urls.json "$HOST:/opt/kakosearch/db/"

echo "[5/5] API 差し替え・再起動..."
ssh "$HOST" 'mv /opt/kakosearch/bin/kakoapi.new /opt/kakosearch/bin/kakoapi &&
  chmod 755 /opt/kakosearch/bin/kakoapi /opt/kakosearch/bin/kakoctl &&
  systemctl restart kakosearch-api &&
  sleep 2 && systemctl is-active kakosearch-api &&
  curl -sf http://127.0.0.1:8080/api/health'
echo
echo "デプロイ完了"
