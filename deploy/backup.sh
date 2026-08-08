#!/bin/bash
# SQLite の定期バックアップ（手元PC=WSL 側から実行。仕様書13.6）。
# VPS 上で VACUUM INTO でスナップショットを作り、E: ドライブへ取得して世代管理する。
# SQLite が唯一の一次データであり、インデックスは再生成できるためバックアップ不要。
set -euo pipefail

HOST=kakovps
REMOTE_DB=/opt/kakosearch/db/kakolog.db
REMOTE_SNAP=/opt/kakosearch/db/backup-snapshot.db
DEST=/mnt/e/kakolog-backup
KEEP=3   # 保持世代数

STAMP=$(date '+%Y%m%d')
mkdir -p "$DEST"

echo "[1/3] VPS 上でスナップショット作成（VACUUM INTO）..."
ssh "$HOST" "rm -f $REMOTE_SNAP && sqlite3 $REMOTE_DB \"VACUUM INTO '$REMOTE_SNAP'\""

echo "[2/3] 取得中（約15GB）..."
rsync -av --partial --progress "$HOST:$REMOTE_SNAP" "$DEST/kakolog.db.bak-$STAMP"
ssh "$HOST" "rm -f $REMOTE_SNAP"

echo "[3/3] 古い世代の削除（最新 $KEEP 世代を保持。初期バックアップ *-20260804 は消さない）..."
ls -1t "$DEST"/kakolog.db.bak-* | grep -v 20260804 | tail -n +$((KEEP + 1)) | xargs -r rm -f
ls -lh "$DEST"
echo "完了"
