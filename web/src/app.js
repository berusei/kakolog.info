/* KakologInfo フロントエンド。依存なしの素の JS。
   API 契約は docs/api-contract-diff.md の裁定に従う。 */
'use strict';

/* ホットワード機能は初版では非実装（仕様書17.2）。
   依頼者指示（2026-08-05）により、枠とタブはデザインどおり表示し中身のみ空にする。
   true にするとデータ描画も有効になる（データ供給の実装が前提）。 */
const FEATURES = { hotWords: false };

const API = '/api';
const SITE_NAME = 'KakologInfo';
// canonical の組み立てに使う。location.origin を使ってはいけない。
// www 付きで来た訪問者のページが「www 版が正規」と自己申告してしまい、
// index.html の canonical による名寄せが台無しになる
const SITE_ORIGIN = 'https://kakolog.info';
// index.html の <title> と一致させること（初期表示・トップへ戻ったときの既定値）
const BASE_TITLE = SITE_NAME + ' - 5ch(2ch)過去ログ検索';
const PER_OPTIONS = [30, 50, 100]; // サーバー側の上限 MaxPer=100 と整合

const $ = (id) => document.getElementById(id);

const state = {
  tab: 'list',       // list | favs | words | about
  // 期間は年→月→日の絞り込み（2026-08-06）。上位が空なら下位も空。
  // 範囲（from〜to）ではなく1つの期間を選ぶ形なので、状態は常に
  // 「全期間 / ある年 / ある年月 / ある年月日」のいずれかにしかならない。
  // 事前判定（board-years.json）の粒度と一致するのはこの性質による。
  q: '', board: '', year: '', month: '', day: '', sort: 'new', page: 1,
  lastResult: null,
  boardsLoaded: false,
};

const settings = load('kakolog.settings', { per: 50, showBoard: true, newTab: true, ng: '' });
// 旧バージョンで保存された表示件数（10/15件）を現行の選択肢へ寄せる
if (!PER_OPTIONS.includes(settings.per)) {
  settings.per = settings.per < 40 ? 30 : settings.per > 75 ? 100 : 50;
}
let favs = load('kakolog.favs2', []); // [{b,k,t,d,u}]

function load(key, def) {
  try {
    const v = JSON.parse(localStorage.getItem(key));
    return v == null ? def : v;
  } catch (e) { return def; }
}
function save(key, v) {
  try { localStorage.setItem(key, JSON.stringify(v)); } catch (e) { /* ignore */ }
}

/* ---------- 初期化 ---------- */

document.addEventListener('DOMContentLoaded', () => {
  initYearOptions();
  initSettingsUI();
  bindEvents();
  loadBoards();
  loadBlockedYears();   // 1KB未満の静的ファイル。検索そのものはこれを待たない
  switchTab('list');
  restoreFromURL();
  updateFavStars();
});

function initYearOptions() {
  const sel = $('year');
  const now = new Date().getFullYear();
  for (let y = now; y >= 1999; y--) {
    const o = document.createElement('option');
    o.value = String(y);
    o.textContent = y + '年';
    sel.appendChild(o);
  }
  const mon = $('month');
  for (let m = 1; m <= 12; m++) {
    const o = document.createElement('option');
    o.value = String(m).padStart(2, '0');
    o.textContent = m + '月';
    mon.appendChild(o);
  }
}

// 日の選択肢はその月の日数に合わせて作り直す。31日固定にすると
// 2月31日のような存在しない日が選べてしまい、サーバーに 400 を返させることになる。
function rebuildDayOptions() {
  const sel = $('day');
  while (sel.options.length > 1) sel.remove(1);   // 先頭の「月内すべて」は残す
  if (!state.year || !state.month) return;
  const days = daysInMonth(parseInt(state.year, 10), parseInt(state.month, 10));
  for (let d = 1; d <= days; d++) {
    const o = document.createElement('option');
    o.value = String(d).padStart(2, '0');
    o.textContent = d + '日';
    sel.appendChild(o);
  }
}

// うるう年を含めた月の日数。Date は「翌月の0日目 = 今月の末日」を返す。
// ここは表示用の選択肢を作るだけで、期間の計算には使わない（期間は文字列のままサーバーへ渡す）。
function daysInMonth(y, m) {
  return new Date(y, m, 0).getDate();
}

// 選択されている期間を API の from/to に変換する。
// ★ 年だけのときは YYYY-MM 形式のまま（既存の共有リンクと同じ形）にしている。
//   サーバーは YYYY-MM と YYYY-MM-DD の両方を受ける（docid.PeriodBounds）。
function periodParams() {
  if (!state.year) return null;
  if (state.month && state.day) {
    const d = state.year + '-' + state.month + '-' + state.day;
    return { from: d, to: d };
  }
  if (state.month) {
    const m = state.year + '-' + state.month;
    return { from: m, to: m };
  }
  return { from: state.year + '-01', to: state.year + '-12' };
}

// 見出し・タイトル用の表記（2017年 / 2017年3月 / 2017年3月5日）
function periodLabel() {
  if (!state.year) return '';
  let s = state.year + '年';
  if (state.month) s += parseInt(state.month, 10) + '月';
  if (state.month && state.day) s += parseInt(state.day, 10) + '日';
  return s;
}

let boardsData = [];
let boardNameById = {};

async function loadBoards() {
  try {
    const res = await fetch(API + '/boards');
    const data = await res.json();
    boardsData = data.boards;
    boardNameById = {};
    boardsData.forEach((b) => { boardNameById[b.board_id] = b.board_name; });

    // カテゴリ別 optgroup。「おすすめ」を先頭、残りはカテゴリ内の合計スレ数降順
    const sel = $('board');
    const byCat = new Map();
    for (const b of boardsData) {
      const cat = b.category || 'その他';
      if (!byCat.has(cat)) byCat.set(cat, []);
      byCat.get(cat).push(b);
    }
    const cats = [...byCat.keys()].sort((a, b) => {
      if (a === 'おすすめ') return -1;
      if (b === 'おすすめ') return 1;
      const sum = (c) => byCat.get(c).reduce((s, x) => s + x.thread_count, 0);
      return sum(b) - sum(a);
    });
    for (const cat of cats) {
      const g = document.createElement('optgroup');
      g.label = cat;
      for (const b of byCat.get(cat)) {
        const o = document.createElement('option');
        o.value = b.board_id;
        o.textContent = b.board_name;
        g.appendChild(o);
      }
      sel.appendChild(g);
    }
    sel.value = state.board;
    state.boardsLoaded = true;
    renderQuickBoards();
    // About の統計とフッター（契約差分 D3）
    $('statThreads').textContent = data.total_threads.toLocaleString('ja-JP');
    $('statBoards').textContent = data.total_boards.toLocaleString('ja-JP');
    if (data.index_updated_at) {
      const d = fmtDate(data.index_updated_at);
      $('statUpdated').textContent = d;
      $('footUpdated').textContent = 'インデックス最終更新: ' + d;
    }
  } catch (e) {
    console.error('boards の取得に失敗', e);
  }
}

const QUICK = ['livejupiter', 'news4vip', 'poverty'];
const QUICK_SHORT = { livejupiter: 'なんJ', news4vip: 'VIP', poverty: '嫌儲' };

function renderQuickBoards() {
  const box = $('quickBoards');
  box.textContent = '';
  const mk = (key, label) => {
    const b = document.createElement('button');
    b.className = 'badge' + (state.board === key ? ' active' : '');
    b.textContent = label;
    b.addEventListener('click', () => {
      state.board = key;
      $('board').value = key;
      state.page = 1;
      renderQuickBoards();
      // 条件を変えただけでは検索しない（依頼者指示 2026-08-06）。理由は bindEvents のコメント
    });
    box.appendChild(b);
  };
  mk('', '全板');
  QUICK.filter((k) => boardNameById[k]).forEach((k) => mk(k, QUICK_SHORT[k] || boardNameById[k]));
}

/* ---------- イベント ---------- */

function bindEvents() {
  $('searchForm').addEventListener('submit', (e) => {
    e.preventDefault();
    state.q = $('q').value;
    state.page = 1;
    doSearch();
  });
  $('q').addEventListener('input', () => {
    $('clearQ').hidden = !$('q').value;
  });
  $('clearQ').addEventListener('click', () => {
    $('q').value = '';
    $('clearQ').hidden = true;
    $('q').focus();
  });
  // 条件（板・年・表示順・表示件数）を変えただけでは検索しない。反映は検索ボタンを
  // 押したときだけ（依頼者指示 2026-08-06）。以前は変更のたびに即検索していたが、
  // 板と年のように2つ選んで初めて意図が決まる操作では、1つ目を選んだ時点で
  // 中途半端な条件のリクエストが飛んでしまう。ページ送りは単独の明示的な操作なので対象外。
  $('board').addEventListener('change', (e) => { state.board = e.target.value; state.page = 1; renderQuickBoards(); });
  // 年→月→日。上位を変えたら下位は必ず捨てる。残すと「2017年3月」から年だけ2018に
  // 変えたときに、見えていない月がそのまま効いて意図しない期間になる
  $('year').addEventListener('change', (e) => {
    state.year = e.target.value;
    state.month = ''; state.day = '';
    state.page = 1;
    syncPeriodUI();
  });
  $('month').addEventListener('change', (e) => {
    state.month = e.target.value;
    state.day = '';
    state.page = 1;
    syncPeriodUI();
  });
  $('day').addEventListener('change', (e) => { state.day = e.target.value; state.page = 1; });
  $('sort').addEventListener('change', (e) => { state.sort = e.target.value; state.page = 1; });

  // タブ（ヘッダー・モバイルメニュー共通）
  document.querySelectorAll('[data-tab]').forEach((el) => {
    el.addEventListener('click', () => switchTab(el.dataset.tab));
  });
  $('favBtn').addEventListener('click', () => switchTab(state.tab === 'favs' ? 'list' : 'favs'));
  $('mFavBtn').addEventListener('click', () => switchTab('favs'));
  $('gearBtn').addEventListener('click', toggleSettings);
  $('mGearBtn').addEventListener('click', toggleSettings);
  $('settingsClose').addEventListener('click', toggleSettings);
  $('menuBtn').addEventListener('click', () => {
    const open = !$('mobileMenu').classList.contains('open');
    $('mobileMenu').classList.toggle('open', open);
    $('menuBtn').classList.toggle('menu-open', open);
    $('menuBtn').setAttribute('aria-expanded', String(open));
    $('settings').hidden = true;
  });

  window.addEventListener('popstate', restoreFromURL);
}

// state に合わせて月・日のセレクトの表示と値をそろえる。
// URL からの復元でも同じ関数を通すことで、操作経由と復元経由で見た目がズレないようにする。
function syncPeriodUI() {
  $('year').value = state.year;
  $('monthBox').hidden = !state.year;
  $('month').value = state.month;
  rebuildDayOptions();
  $('dayBox').hidden = !state.year || !state.month;
  $('day').value = state.day;
}

function toggleSettings() {
  const el = $('settings');
  el.hidden = !el.hidden;
  $('gearBtn').classList.toggle('active', !el.hidden);
  $('gearBtn').setAttribute('aria-expanded', String(!el.hidden));
  $('mobileMenu').classList.remove('open');
  $('menuBtn').classList.remove('menu-open');
}

function initSettingsUI() {
  $('setPer').value = String(settings.per);
  $('setShowBoard').checked = settings.showBoard;
  $('setNewTab').checked = settings.newTab;
  $('setNg').value = settings.ng;
  updateNgCount();
  $('setPer').addEventListener('change', (e) => { settings.per = parseInt(e.target.value, 10); save('kakolog.settings', settings); state.page = 1; });
  $('setShowBoard').addEventListener('change', (e) => { settings.showBoard = e.target.checked; save('kakolog.settings', settings); rerender(); });
  $('setNewTab').addEventListener('change', (e) => { settings.newTab = e.target.checked; save('kakolog.settings', settings); rerender(); });
  $('setNg').addEventListener('change', (e) => { settings.ng = e.target.value; save('kakolog.settings', settings); updateNgCount(); rerender(); });
}

function ngList() {
  return settings.ng.split(/[\n,、]+/).map((w) => w.trim()).filter(Boolean);
}
function updateNgCount() {
  const n = ngList().length;
  $('ngCount').textContent = n ? n + ' 語を除外中' : '未設定';
}

/* ---------- タブ切り替え ---------- */

function switchTab(tab) {
  state.tab = tab;
  const views = { list: 'viewList', favs: 'viewFavs', words: 'viewWords', about: 'viewAbout' };
  for (const [k, id] of Object.entries(views)) {
    const el = $(id);
    if (el) el.hidden = k !== tab;
  }
  document.querySelectorAll('[data-tab]').forEach((el) => {
    el.classList.toggle('active', el.dataset.tab === tab);
  });
  $('favBtn').classList.toggle('active', tab === 'favs');
  // サイドバー（話題のキーワード枠）は一覧タブでのみ表示（デザインどおり）
  const rail = $('rail');
  if (rail) {
    rail.hidden = tab !== 'list';
    $('main').classList.toggle('with-rail', tab === 'list');
  }
  $('mobileMenu').classList.remove('open');
  $('menuBtn').classList.remove('menu-open');
  if (tab === 'favs') renderFavs();
  updatePageTitle();
  window.scrollTo(0, 0);
}

/* ---------- 検索 ---------- */

// 1文字クエリの送信前バリデーション（仕様書17.1: サーバーの400をそのまま見せない）
function isSingleCharQuery(q) {
  const words = q.split(/[\s　]+/).filter((w) => w && !(w.startsWith('-') && w.length > 1));
  const joined = words.join('').normalize('NFKC').replace(/[\s"「」']/g, '');
  return Array.from(joined).length === 1;
}

// 検索の通し番号。板や年を変えた直後にすぐ検索し直すと、前のリクエストが
// あとから返ってきて新しい状態を上書きしうる（結果・タイトル・URLが食い違う）。
// 自分より新しい検索が始まっていたら、返ってきた応答は捨てる。
let searchSeq = 0;

// スピナーを出すまでの待ち時間。ウォーム時の p95 は 60〜130ms（docs/benchmark.md）で、
// 即座に出すと大半の検索でスピナーが一瞬光るだけのちらつきになる。
// コールド時（1秒前後）や回線が細いときにだけ出るようにする。
const LOADING_DELAY_MS = 180;
let loadingTimer = null;

function startLoading(seq) {
  clearTimeout(loadingTimer);
  loadingTimer = setTimeout(() => {
    if (seq !== searchSeq) return;   // 追い越された検索のスピナーは出さない
    // 前回の検索結果を必ず片付けてからスピナーを出す。残したままだと、
    // それが今の検索語の結果に見えてしまう（scopeLine の見出しと件数も同じ理由で消す）。
    // 消すのは検索開始時ではなくスピナーを出す瞬間。開始時に消すと、
    // 速い検索（p95 60〜130ms）で結果が一瞬空白になって点滅する。
    $('resultsCard').hidden = true;
    $('scopeLine').hidden = true;
    $('pagination').hidden = true;
    $('loadingCard').hidden = false;
  }, LOADING_DELAY_MS);
}

// 呼ぶのは「自分が最新の検索である」と確認できた側だけにすること。
// 追い越された古い応答がこれを呼ぶと、進行中の新しい検索のスピナーを消してしまう。
function stopLoading() {
  clearTimeout(loadingTimer);
  loadingTimer = null;
  $('loadingCard').hidden = true;
}

// 検索語なしの一覧が使える条件（板と年の両方が指定されていること）。
// サーバー側の browse 判定（cmd/kakoapi/server.go）と揃えること。片方だけずれると、
// 出せないはずの一覧を投げて 400 を見せる／出せる一覧を出さない、のどちらかになる。
// 件数が25万件を超える組み合わせはサーバーが 400 で跳ね返すため、その案内はそのまま表示する。
function canBrowse() {
  return !state.q.trim() && !!state.board && !!state.year;
}

// 応答のキャッシュ。キーはリクエストのクエリ文字列そのもの。
// 上限を切っているのは、長く使ったときに際限なく溜めないため（1応答は最大100件）。
// res_count は時間とともに増えるが、セッション中の数分のズレは実用上問題にならない。
const searchCache = new Map();
const SEARCH_CACHE_MAX = 40;
function cachePut(key, data) {
  if (searchCache.size >= SEARCH_CACHE_MAX) {
    searchCache.delete(searchCache.keys().next().value);   // 最も古いものから捨てる
  }
  searchCache.set(key, data);
}

// 一覧できない (板, 年) の一覧。kakoctl board-years が生成する静的ファイル。
// サーバーに聞きに行かず、押された時点で弾くために使う（依頼者提案 2026-08-06）。
//
// ★ これは UI のヒントであって保証ではない。件数の出どころは SQLite（スクレイパーが
// 更新し続ける）だが、一覧の実体は月次再構築の索引なので、当年ぶんは必ずズレる。
// ここを通り抜けたものはサーバーの 400 too_many_results が最終的に受け止める。
let blockedYears = {};
let blockedMonths = {};
async function loadBlockedYears() {
  try {
    const res = await fetch('/board-years.json', { cache: 'no-cache' });
    if (!res.ok) return;   // 未生成でも検索そのものは動く。その場合はサーバーが弾く
    const data = await res.json();
    blockedYears = data.blocked || {};
    blockedMonths = data.blocked_months || {};   // 月の記録が無い旧世代のファイルでも動く
  } catch (e) {
    console.error('board-years.json の取得に失敗', e);
  }
}

// 選択中の期間が一覧できないと分かっているか。
// ★ 判定できるのは記録と粒度が一致するときだけ。年→月→日の絞り込みなので、
//   選択は常に「年ちょうど」「月ちょうど」「日ちょうど」のいずれかになり、必ず一致する。
// ★ 日は判定しない。1日で25万件を超えるには1板で25万スレ立つ必要があり、
//   最盛期の news4vip でも1日あたり約1.3万件で桁が2つ足りない（board-years.json も記録しない）。
function isBlockedPeriod(board, year, month, day) {
  if (!board || !year) return false;
  if (day) return false;
  if (month) {
    const ms = blockedMonths[board];
    return !!ms && ms.indexOf(year + '-' + month) !== -1;
  }
  const ys = blockedYears[board];
  return !!ys && ys.indexOf(parseInt(year, 10)) !== -1;
}

async function doSearch() {
  const seq = ++searchSeq;
  const q = state.q.trim();
  const browse = canBrowse();
  if (!q && !browse) {
    // 板・年が揃っていないのに検索語もない場合。黙って何もしないと
    // 「検索できる条件」が利用者に伝わらないので、一覧の条件を案内する
    showGuidance('検索語を入力してください。板と年の両方を選ぶと、検索語なしでも一覧できます。');
    return;
  }

  if (q && isSingleCharQuery(q) && !state.board && !state.year) {
    showGuidance('1文字での検索は、板または年の指定が必要です。左下のプルダウンから選んでください。');
    return;
  }

  // 一覧できないと分かっている期間は、サーバーに聞かずにここで弾く。
  // 判定材料は board-years.json（静的ファイル。nginx が返すので searchd も SQLite も動かない）
  if (browse && isBlockedPeriod(state.board, state.year, state.month, state.day)) {
    showGuidance('この板の' + periodLabel() + 'は、スレッド数が25万件を超えるため一覧できません。'
      + (state.month ? '日を指定するか、' : '月を指定するか、')
      + '検索語を入れて絞り込んでください。');
    return;
  }

  const params = new URLSearchParams({ sort: state.sort, page: String(state.page), per: String(settings.per) });
  if (q) params.set('q', q);   // 一覧モードでは q を送らない（送ると空語として 400 になる）
  if (state.board) params.set('board', state.board);
  const period = periodParams();
  if (period) {
    params.set('from', period.from);
    params.set('to', period.to);
  }

  hideNotice();
  $('welcome').hidden = true;
  syncURL(params);

  // 同じ条件をもう一度引いたとき（表示順の行き来、ページを戻る等）はリクエストしない。
  // 表示順の切り替えは手元の結果を反転しても実現できない点に注意。最新順の1ページ目と
  // 古い順の1ページ目は別の集合であり（仕様書4.1.1でインデックスを2本建てている理由）、
  // ここでキャッシュしているのは「同一条件の応答」であって並べ替えではない。
  const key = params.toString();
  const hit = searchCache.get(key);
  if (hit) {
    state.lastResult = hit;
    renderResults(hit);
    updatePageTitle();
    return;
  }
  startLoading(seq);

  let res, data;
  try {
    res = await fetch(API + '/search?' + key);
    data = await res.json();
  } catch (e) {
    if (seq !== searchSeq) return;
    showGuidance('サーバーに接続できませんでした。時間をおいてお試しください。', true);
    return;
  }
  if (seq !== searchSeq) return;   // 追い越された古い応答は捨てる
  stopLoading();
  if (!res.ok) {
    // API のエラーメッセージはそのまま表示できる日本語（仕様書10.4）
    showGuidance((data.error && data.error.message) || 'エラーが発生しました。', res.status >= 500);
    return;
  }
  cachePut(key, data);
  state.lastResult = data;
  renderResults(data);
  updatePageTitle();
}

// 検索が成立しなかったとき（1文字ガード・件数超過・サーバーエラー）の表示。
// 案内だけ出して前回の検索結果を残すと、その結果が今の検索語のものに見えてしまうため、
// 結果まわりを必ず消してから案内を出す。
function showGuidance(msg, isError) {
  stopLoading();   // 呼び出し元は全て「自分が最新の検索」を確認済み
  state.lastResult = null;
  updatePageTitle();   // 結果が無い状態なので既定のタイトルへ戻す
  $('resultsCard').hidden = true;
  $('scopeLine').hidden = true;
  $('pagination').hidden = true;
  $('welcome').hidden = true;
  showNotice(msg, !!isError);
}

function rerender() {
  if (state.tab === 'favs') renderFavs();
  else if (state.lastResult) renderResults(state.lastResult);
}

function renderResults(data) {
  const rowsBox = $('rows');
  rowsBox.textContent = '';
  const ng = ngList();
  const terms = state.q.split(/[\s　]+/)
    .filter((w) => w && !(w.startsWith('-') && w.length > 1))
    .map((w) => w.replace(/^["「']+|["」']+$/g, ''))
    .filter(Boolean);

  let shown = 0;
  for (const item of data.items) {
    if (ng.some((w) => item.title.includes(w))) continue;
    rowsBox.appendChild(renderRow(item, terms));
    shown++;
  }

  const boardLabel = state.board ? '「' + boardName(state.board) + '」' : '「すべての板」';
  const yearLabel = state.year ? ' / ' + periodLabel() : '';
  // 一覧モードでは API が query に空文字を返す。「「」の検索結果」と出さないよう見出しを分ける
  $('scopeLabel').textContent = data.query
    ? boardLabel + ' / 「' + data.query + '」' + yearLabel + ' の検索結果'
    : boardLabel + yearLabel + ' のスレッド一覧';
  $('countLabel').textContent = (data.total_is_approximate ? '約 ' : '') + data.total.toLocaleString('ja-JP') + ' 件' + (data.total_is_approximate ? '以上' : '');
  $('scopeLine').hidden = false;
  $('resultsCard').hidden = false;
  $('emptyState').hidden = shown !== 0;
  renderPagination(data);
  window.scrollTo(0, 0);
}

function boardName(id) {
  return boardNameById[id] || id;
}

function renderRow(item, terms) {
  const row = document.createElement('div');
  row.className = 'row';

  const a = document.createElement('a');
  a.className = 'row-link';
  a.href = item.url;
  a.rel = 'noopener';
  a.target = settings.newTab ? '_blank' : '_self';

  const title = document.createElement('span');
  title.className = 'row-title';
  appendHighlighted(title, item.title, terms);
  const res = document.createElement('span');
  res.className = 'row-res';
  res.textContent = ' (' + item.res_count + ')';
  title.appendChild(res);

  const meta = document.createElement('span');
  meta.className = 'row-meta';
  const time = document.createElement('time');
  time.textContent = fmtDateTime(item.created_at);
  meta.appendChild(time);
  if (settings.showBoard) {
    const b = document.createElement('span');
    b.className = 'row-board';
    b.textContent = item.board_name === item.board_id ? item.board_id : item.board_name;
    meta.appendChild(b);
  }
  a.appendChild(title);
  a.appendChild(meta);

  const fav = document.createElement('button');
  fav.className = 'fav-btn';
  fav.title = 'お気に入り';
  const star = document.createElement('span');
  star.className = 'fs' + (isFav(item.board_id, item.thread_key) ? ' on' : '');
  star.textContent = '★';
  fav.appendChild(star);
  fav.addEventListener('click', () => {
    toggleFav(item);
    star.classList.toggle('on', isFav(item.board_id, item.thread_key));
    updateFavStars();
  });

  row.appendChild(a);
  row.appendChild(fav);
  return row;
}

// タイトル中の検索語を <span class="hl"> で強調する。DOM 構築のみで innerHTML は使わない
function appendHighlighted(parent, title, terms) {
  if (!terms.length) {
    parent.appendChild(document.createTextNode(title));
    return;
  }
  let i = 0;
  let plain = '';
  const flush = () => {
    if (plain) { parent.appendChild(document.createTextNode(plain)); plain = ''; }
  };
  while (i < title.length) {
    let hit = null;
    for (const w of terms) {
      if (title.startsWith(w, i) && (!hit || w.length > hit.length)) hit = w;
    }
    if (hit) {
      flush();
      const s = document.createElement('span');
      s.className = 'hl';
      s.textContent = hit;
      parent.appendChild(s);
      i += hit.length;
    } else {
      plain += title[i];
      i++;
    }
  }
  flush();
}

function renderPagination(data) {
  const nav = $('pagination');
  nav.textContent = '';
  const maxPage = data.max_page;
  if (maxPage <= 1) { nav.hidden = true; return; }
  nav.hidden = false;

  const mkBtn = (label, opts) => {
    const b = document.createElement('button');
    b.className = 'btn btn-outline' + (opts.active ? ' active' : '') + (opts.ellipsis ? ' ellipsis' : '');
    b.textContent = label;
    if (opts.disabled) b.disabled = true;
    if (opts.go) b.addEventListener('click', () => { state.page = opts.go; doSearch(); });
    nav.appendChild(b);
  };

  mkBtn('前へ', { disabled: data.page <= 1, go: data.page - 1 });
  let lastShown = 0;
  for (let n = 1; n <= maxPage; n++) {
    if (n === 1 || n === maxPage || Math.abs(n - data.page) <= 2) {
      if (lastShown && n - lastShown > 1) mkBtn('…', { ellipsis: true });
      mkBtn(String(n), { active: n === data.page, go: n === data.page ? null : n });
      lastShown = n;
    }
  }
  mkBtn('次へ', { disabled: data.page >= maxPage, go: data.page + 1 });

  if (data.page === maxPage && data.total > maxPage * data.per_page) {
    const note = document.createElement('span');
    note.style.cssText = 'font-size:11px;color:#86868b;width:100%;text-align:center;padding-top:4px';
    note.textContent = '表示できるのはここまでです。さらに探すには検索条件を絞り込んでください。';
    nav.appendChild(note);
  }
}

/* ---------- お気に入り（契約差分 D6: 端末ローカルの保存リスト） ---------- */

function favKey(b, k) { return b + '/' + k; }
function isFav(b, k) { return favs.some((f) => f.b === b && f.k === k); }

function toggleFav(item) {
  if (isFav(item.board_id, item.thread_key)) {
    favs = favs.filter((f) => !(f.b === item.board_id && f.k === item.thread_key));
  } else {
    favs.push({ b: item.board_id, k: item.thread_key, t: item.title, d: item.created_at, u: item.url, n: item.board_name, r: item.res_count });
  }
  save('kakolog.favs2', favs);
}

function updateFavStars() {
  const on = favs.length > 0;
  $('favBtnStar').classList.toggle('on', on);
  $('mFavStar').classList.toggle('on', on);
}

function renderFavs() {
  const box = $('favRows');
  box.textContent = '';
  $('favEmpty').hidden = favs.length !== 0;
  for (const f of [...favs].reverse()) {
    const item = { board_id: f.b, thread_key: f.k, title: f.t, created_at: f.d, url: f.u, board_name: f.n || f.b, res_count: f.r || 0 };
    const row = renderRow(item, []);
    box.appendChild(row);
  }
}

/* ---------- URL 状態・ユーティリティ ---------- */

function syncURL(params) {
  history.pushState(null, '', '?' + params.toString());
  setNoindex(true);
}

// 検索結果ページは noindex（仕様書13.4・17.3）。クローラに深いページネーションを
// 踏まれるとサービスが停止しうる。nginx も X-Robots-Tag で同じ判定をしており、
// こちらは JS を実行するクローラ向けの二重の保険。トップと板一覧は対象外。
//
// あわせて canonical も面倒を見る。noindex のページが別URL（トップ）を canonical に
// 指していると「このURLは見るな、ただし正規は別」という矛盾した指示になり、
// noindex が正規側（＝トップ）に波及するおそれがある。検索結果では canonical を
// 自己参照（現在のURL）に切り替えて、noindex の対象をそのURLだけに閉じる。
function setNoindex(on) {
  let m = document.querySelector('meta[name="robots"]');
  if (!m) {
    m = document.createElement('meta');
    m.name = 'robots';
    document.head.appendChild(m);
  }
  m.content = on ? 'noindex, follow' : 'index, follow';

  const link = document.querySelector('link[rel="canonical"]');
  if (link) link.href = on ? SITE_ORIGIN + location.pathname + location.search : SITE_ORIGIN + '/';
}

// ブラウザのタブ・履歴・ブックマーク・共有時のリンク名になる。
// OGP と違い <title> は JS の更新が反映される場面が多いので、状態に追従させる。
function updatePageTitle() {
  const t = (s) => { document.title = s; };
  if (state.tab === 'about') return t('このサイトについて - ' + SITE_NAME);
  if (state.tab === 'favs') return t('お気に入り一覧 - ' + SITE_NAME);
  if (state.tab === 'words') return t('人気ワード - ' + SITE_NAME);
  if (state.tab === 'list' && state.lastResult) {
    const cond = [state.board ? boardName(state.board) : '', periodLabel()]
      .filter(Boolean).join(' ');
    const page = state.page > 1 ? ' - ' + state.page + 'ページ目' : '';
    // 一覧モード（検索語なし）は query が空。条件そのものが見出しになる
    if (!state.lastResult.query) {
      return t(cond + ' のスレッド一覧' + page + ' - ' + SITE_NAME);
    }
    return t('「' + state.lastResult.query + '」の検索結果'
      + (cond ? '（' + cond + '）' : '')
      + page
      + ' - ' + SITE_NAME);
  }
  t(BASE_TITLE);
}

function restoreFromURL() {
  const p = new URLSearchParams(location.search);
  // 一覧モードの URL には q が無い。板と期間の両方が載っているかで見分ける
  // 期間は年・月・日のいずれの粒度でも載りうる（YYYY-MM / YYYY-MM-DD）
  const isBrowseURL = !!p.get('board')
    && /^\d{4}-\d{2}(-\d{2})?$/.test(p.get('from') || '')
    && /^\d{4}-\d{2}(-\d{2})?$/.test(p.get('to') || '');
  if (!p.get('q') && !isBrowseURL) {
    // 「戻る」でトップへ帰ってきた場合。インデックス可の状態に戻す
    state.lastResult = null;
    setNoindex(false);
    updatePageTitle();
    return;
  }
  setNoindex(true);   // 検索結果・一覧の URL で直接開かれた場合（クローラを含む）
  state.q = p.get('q') || '';
  state.board = p.get('board') || '';
  state.sort = p.get('sort') === 'old' ? 'old' : 'new';
  state.page = Math.max(1, parseInt(p.get('page'), 10) || 1);
  applyPeriodFromURL(p.get('from') || '', p.get('to') || '');
  $('q').value = state.q;
  $('clearQ').hidden = !state.q;
  $('sort').value = state.sort;
  syncPeriodUI();
  if (state.boardsLoaded) $('board').value = state.board;
  doSearch();
}

// URL の from/to を年・月・日に戻す。periodParams() の逆変換。
// 期間は必ず「年ちょうど / 月ちょうど / 日ちょうど」のいずれかで書き出しているので、
// from と to の形を見れば一意に決まる。当てはまらない形（手で書き換えられた範囲指定など）は
// 全期間として扱う。サーバーは範囲を受け付けるが、UI では表現できないため。
function applyPeriodFromURL(from, to) {
  state.year = state.month = state.day = '';
  const day = /^(\d{4})-(\d{2})-(\d{2})$/.exec(from);
  if (day && from === to) {
    state.year = day[1]; state.month = day[2]; state.day = day[3];
    return;
  }
  const mon = /^(\d{4})-(\d{2})$/.exec(from);
  if (mon && from === to) {
    state.year = mon[1]; state.month = mon[2];
    return;
  }
  // 年まるごと（既存の共有リンクもこの形。互換のため必ず残すこと）
  if (mon && mon[2] === '01' && to === mon[1] + '-12') {
    state.year = mon[1];
  }
}

function fmtDate(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso);
  return m ? `${m[1]}/${m[2]}/${m[3]}` : iso;
}

function fmtDateTime(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(iso);
  return m ? `${m[1]}/${m[2]}/${m[3]} ${m[4]}:${m[5]}:${m[6]}` : iso;
}

function showNotice(msg, isError) {
  const n = $('notice');
  n.textContent = msg;
  n.classList.toggle('error', !!isError);
  n.hidden = false;
}
function hideNotice() { $('notice').hidden = true; }
