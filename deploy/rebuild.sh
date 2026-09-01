#!/bin/bash
# 夜間再構築（VPS 上で root で実行 / cron 登録用）。
# スクレイパーが SQLite を更新した後に呼ぶと、新規スレが検索に反映される（仕様書9.2）。
# 流れ: 板マスタ更新 → kako_new/kako_old を再構築 → ローテーション → メタ情報スタンプ
set -euo pipefail

KAKOCTL=/opt/kakosearch/bin/kakoctl
DB=/opt/kakosearch/db/kakolog.db
CONF=/etc/manticoresearch/manticore.conf
DATA=/var/lib/manticore/data
PIDFILE=/var/run/manticore/searchd.pid
LOG_TAG="kako-rebuild"

log() { echo "[$(date '+%F %T')] $*"; logger -t "$LOG_TAG" "$*" || true; }

# 1本のテーブル構築に必要な空き容量。ソート一時ファイル(約14GB)と新世代(約16GB)が
# 同時に存在する瞬間があるため、これを下回った状態で indexer を起動してはならない。
# 途中でディスクが尽きると indexer は黙って死に、1時間の構築が無駄になる
# （初回リハーサル 2026-08-05 で実際に発生）。
NEED_GB=32

# 0. 前回実行の残骸を掃除して容量を確保する。
#    - .old: 前回のローテーションは件数チェックを通過して確定済み。索引は SQLite から
#      いつでも再生成できるため、これを保持し続ける価値より容量の方が重要（仕様書13.6）
#    - .tmp: 異常終了した indexer が残したソート一時ファイル（最大14GB）
rm -f "$DATA"/kako_*.old.* "$DATA"/kako_*.tmp.*

check_disk() {
  local avail
  avail=$(df -BG --output=avail "$DATA" | tail -1 | tr -dc '0-9')
  if [ "$avail" -lt "$NEED_GB" ]; then
    log "中止: 空き ${avail}GB < 必要 ${NEED_GB}GB"
    return 1
  fi
  log "空き ${avail}GB"
}
check_disk

# 2. 新規スレのスクレイプ（板ごとの過去ログ一覧 kakoNNNN.html から追加分を取得して SQLite へ）
#    個別板の失敗は stderr に残して続行する（アンカーが進まないため次回自動で追い付く）。
#    全板失敗（ネットワーク断など）のときのみ非0で止まり、以降の再構築を行わない。
#    2026-08-21 追加: 5ch の過去ログ倉庫が止まっている板は 2ch.sc から補完する
#    （docs/changes/2026-08-21.md）。対応表は毎回 BBSMENU から作り直す（1リクエスト）。
#    失敗しても既存の JSON で続行する。板サーバーの移転はここでしか追随できないため、
#    「取れなかったから対応表を消す」ことは絶対にしない（scboards は原子的に置換する）。
log "scboards 更新開始"
"$KAKOCTL" scboards --urls /opt/kakosearch/db/board-urls.json \
  --out /opt/kakosearch/db/board-urls-sc.json || log "警告: scboards に失敗（既存の対応表で続行）"

log "scrape 開始"
"$KAKOCTL" scrape --db "$DB" --urls /opt/kakosearch/db/board-urls.json \
  --sc-urls /opt/kakosearch/db/board-urls-sc.json

# 3. 新規板があれば board_idx を末尾に追加し、表示名も更新（仕様書16.2: 既存の idx は不変）
log "boards 更新開始"
"$KAKOCTL" boards --db "$DB" --names /opt/kakosearch/db/boards.json

# 4. 再構築 + ローテーション（searchd は seamless_rotate で無停止差し替え）。
#    本スクリプトは root で動くため indexer の生成物が root 所有になり、indexer 自身が送る
#    SIGHUP では searchd（manticore ユーザー）が .new を開けずローテーションに失敗する。
#    そこで構築後に所有権を直して SIGHUP を再送し、完了と件数を確認してから旧世代を消す。
#    （初回リハーサル 2026-08-05 で実際に発生した問題への恒久対応）
rebuild_table() {
  local t=$1
  # 2本目の開始前にも必ず確認する。1本目の .old 削除で容量が戻っているはずだが、
  # 戻っていなければここで止める方が、1時間かけて途中で落ちるより良い
  check_disk
  log "$t 構築開始"
  indexer --config "$CONF" --rotate "$t"

  if ls "$DATA/$t".new.spd >/dev/null 2>&1; then
    chown manticore:manticore "$DATA/$t".new.*
    kill -HUP "$(cat "$PIDFILE")"
    for _ in $(seq 1 60); do
      ls "$DATA/$t".new.spd >/dev/null 2>&1 || break
      sleep 5
    done
    if ls "$DATA/$t".new.spd >/dev/null 2>&1; then
      log "エラー: $t のローテーションが完了しない（.new が残存）。searchd のログを確認"
      return 1
    fi
  fi

  # 件数のサニティチェックが通ってから旧世代を削除する（容量確保。仕様書16.5の同居分）
  local cnt
  cnt=$(curl -s "http://127.0.0.1:9308/cli_json" -d "SELECT COUNT(*) FROM $t" \
        | sed -n 's/.*"count(\*)":\([0-9]\+\).*/\1/p' | head -1)
  if [ -z "$cnt" ] || [ "$cnt" -lt 100000000 ]; then
    log "エラー: $t の件数チェック失敗（cnt=${cnt:-空}）。旧世代 .old を保持したまま中止"
    return 1
  fi
  log "$t ローテーション完了（${cnt}件）"
  rm -f "$DATA/$t".old.*

  # ウォームアップ: 差し替え直後は新世代のページキャッシュが空で、最初の数クエリが
  # 3秒タイムアウトに掛かって 503 になる（リハーサルで確認）。頻出トークンを数回引いて
  # 辞書・スキップリストをキャッシュに載せておく。失敗しても再構築の成否には影響させない
  for w in "チ ー ズ" "の" "ま と め" "ス レ"; do
    curl -s -o /dev/null --max-time 20 "http://127.0.0.1:9308/cli_json" \
      -d "SELECT id FROM $t WHERE MATCH('@title \"$w\"') ORDER BY id ASC LIMIT 50 OPTION ranker=none" || true
  done
}

rebuild_table kako_new
rebuild_table kako_old

# 5. 正規化バージョンと構築時刻を記録（仕様書16.3。API の起動時照合とフッター表示に使う）
"$KAKOCTL" stamp --db "$DB"

# 6. 検索語なしの一覧ができない (板, 年) を書き出す（フロントが押される前に弾くための静的ファイル）。
#    索引を差し替えた直後に作ることで、件数と索引の中身のズレを最小にする。
#    ★ このファイルは deploy.sh の rsync --delete の対象外にしてある（web/dist には無いため）。
#    失敗しても検索は動く（サーバー側の 400 too_many_results が最終的に受け止める）ので、
#    ここで再構築全体を失敗扱いにはしない。
log "board-years 生成開始"
"$KAKOCTL" board-years --db "$DB" --out /opt/kakosearch/web/board-years.json || \
  log "警告: board-years の生成に失敗（検索は継続可能。次回再構築で再試行される）"

log "再構築完了"
