#!/bin/bash
# ローカル開発環境の起動（手元PC=WSL 用）。
#
#   ./build/dev.sh          http://localhost:8088/ で待ち受ける
#   ./build/dev.sh 9000     ポート指定
#
# web/src をそのまま配信し、/api/ は本番 VPS へ中継する（build/dev.py）。
# ローカルには SQLite も Manticore も置かない。
#
# 【2026-08-05 の構成変更】以前は Docker の Manticore + ローカル SQLite で
# フルスタックを再現していたが、VPS を一次データとする方針に伴い 64GB のローカル
# データを削除したため、フロントエンド専用の構成に切り替えた。
# バックエンド（Go）の変更を試すには VPS へデプロイするか、
# db/ と build/full/ を VPS から復元する必要がある（docs/DEPLOY.md 参照）。
set -euo pipefail
exec python3 "$(dirname "$0")/dev.py" "$@"
