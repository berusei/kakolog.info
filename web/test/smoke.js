/* フロントエンドのスモークテスト（jsdom）。
 *
 *   node web/test/smoke.js        # web/src を検査
 *   node web/test/smoke.js <path> # 任意のディレクトリ（本番から落とした app.js の検証など）
 *
 * 目的は「画面に出るところまで到達するか」を見ること。go test はサーバー側しか見ないため、
 * 「API は正常・JS は構文的に正しい・でも描画に到達しない」類のバグを原理的に捕まえられない。
 * 実際 2026-08-06 に cachePut の無限再帰（RangeError）を本番へ出してしまい、
 * 検索結果が一切表示されない状態になった。node --check は構文しか見ないので通ってしまう。
 * その回帰をここで止める。
 *
 * fetch は全て差し替えるので、サーバーも DB も要らない。
 */
'use strict';

const fs = require('fs');
const path = require('path');

let JSDOM;
try {
  ({ JSDOM } = require('jsdom'));
} catch (e) {
  console.error('jsdom がありません。`npm install` を実行してください。');
  process.exit(2);
}

const SRC = process.argv[2] || path.join(__dirname, '..', 'src');

// 描画の途中で例外が飛ぶと jsdom の非同期処理の中で落ちる（2026-08-06 の RangeError がこれ）。
// 生のスタックトレースだけ出て終わると原因が読み取りにくいので、必ず理由を添えて落とす。
process.on('uncaughtException', (e) => {
  console.log('\n★ フロントエンドの実行時エラーでテストが中断しました');
  console.log('  ' + e.message);
  console.log('  → 画面の描画に到達していません。app.js を確認してください。');
  process.exit(1);
});

let failed = 0;
function check(label, cond, detail) {
  if (cond) {
    console.log('  ok   ' + label);
  } else {
    failed++;
    console.log('  FAIL ' + label + (detail === undefined ? '' : '  -> ' + JSON.stringify(detail)));
  }
}

// --- テスト用の応答 ---------------------------------------------------------

const BOARDS = {
  boards: [{ board_id: 'livejupiter', board_name: 'なんでも実況J', category: 'おすすめ', thread_count: 34938574 },
    { board_id: 'zoid', board_name: 'ゾイド', category: 'その他', thread_count: 4716 }],
  total_threads: 136377868, total_boards: 902, index_updated_at: '2026-08-06T06:14:57+09:00',
};
const BLOCKED = {
  window: 250000,
  blocked: { livejupiter: [2015, 2017] },
  blocked_months: { livejupiter: ['2017-03'] },
};

function items(n, board) {
  return Array.from({ length: n }, (_, i) => ({
    board_id: board, board_name: board === 'zoid' ? 'ゾイド' : 'なんでも実況J',
    thread_key: String(1785949563 - i), title: 'テストスレ' + i, res_count: 100 + i,
    created_at: '2026-08-06T12:34:56+09:00',
    url: 'https://x/test/read.cgi/' + board + '/1/',
  }));
}

// 1回分の環境を作る。fetchLog に投げた URL が溜まる
function boot(url) {
  const html = fs.readFileSync(path.join(SRC, 'index.html'), 'utf8');
  const app = fs.readFileSync(path.join(SRC, 'app.js'), 'utf8');
  const dom = new JSDOM('<!doctype html><html><head></head><body>' + html + '</body></html>', {
    url: url || 'http://localhost:8088/', runScripts: 'outside-only', pretendToBeVisual: true,
  });
  const w = dom.window;
  const fetchLog = [];
  const errors = [];
  let nextSearch = { status: 200, body: null };

  w.fetch = async (url) => {
    fetchLog.push(url);
    if (url.includes('/api/boards')) return { ok: true, status: 200, json: async () => BOARDS };
    if (url.includes('board-years.json')) return { ok: true, status: 200, json: async () => BLOCKED };
    const r = nextSearch;
    return { ok: r.status === 200, status: r.status, json: async () => r.body };
  };
  w.matchMedia = () => ({ matches: false, addEventListener() {} });
  w.scrollTo = () => {};
  w.addEventListener('error', (e) => errors.push(String(e.message)));
  // 未処理の例外を握りつぶさない。今回のバグはここに出る
  const origError = w.console.error;
  w.console.error = (...a) => { errors.push(a.map(String).join(' ')); origError(...a); };

  try {
    w.eval(app);
  } catch (e) {
    errors.push('app.js の読み込みで例外: ' + e.message);
  }
  w.document.dispatchEvent(new w.Event('DOMContentLoaded'));

  return {
    w, fetchLog, errors,
    $: (id) => w.document.getElementById(id),
    setSearch(status, body) { nextSearch = { status, body }; },
    // 条件を選んで検索ボタンを押す。期間は年→月→日の順に選ぶ（UI と同じ操作）
    search({ q = '', board = '', year = '', month = '', day = '' }) {
      const $ = (id) => w.document.getElementById(id);
      if (board) { $('board').value = board; $('board').dispatchEvent(new w.Event('change')); }
      if (year) { $('year').value = year; $('year').dispatchEvent(new w.Event('change')); }
      if (month) { $('month').value = month; $('month').dispatchEvent(new w.Event('change')); }
      if (day) { $('day').value = day; $('day').dispatchEvent(new w.Event('change')); }
      $('q').value = q;
      $('searchForm').dispatchEvent(new w.Event('submit'));
    },
  };
}

const tick = (ms) => new Promise((r) => setTimeout(r, ms));

// --- テスト -----------------------------------------------------------------

async function testKeywordSearch() {
  console.log('検索結果が描画される');
  const t = boot();
  await tick(50);
  t.setSearch(200, {
    query: 'チーズ', total: 117420, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 2348, index_updated_at: BOARDS.index_updated_at, took_ms: 42, items: items(50, 'zoid'),
  });
  t.search({ q: 'チーズ' });
  await tick(120);

  // ★ これが 2026-08-06 の回帰。描画に到達しないと 0 になる
  check('50件が描画される', t.$('rows').children.length === 50, t.$('rows').children.length);
  check('結果カードが表示される', t.$('resultsCard').hidden === false);
  check('件数が出る', t.$('countLabel').textContent.includes('117,420'), t.$('countLabel').textContent);
  check('スピナーが消えている', t.$('loadingCard').hidden === true);
  check('JS エラーが出ていない', t.errors.length === 0, t.errors);

  // 日時は yyyy/mm/dd hh:mm:ss（2026-08-06 追加）。
  // ★ 期待値を固定値で比較しているのは意図的。created_at は +09:00 付きなので、
  //   new Date(iso).toLocaleString() 方式に書き換えると閲覧者のタイムゾーンに変換され、
  //   JST 以外の環境で別の時刻になる（仕様書付録B: TIMEZONE = Asia/Tokyo 固定）。
  //   このテストは実行マシンの TZ に関わらず落ちるので、その退行を捕まえられる。
  const time = t.$('rows').children[0].querySelector('time');
  check('日時が秒まで出る（JST固定）', time && time.textContent === '2026/08/06 12:34:56',
    time && time.textContent);
}

async function testBrowse() {
  console.log('検索語なしの一覧（板＋年）');
  const t = boot();
  await tick(50);
  t.setSearch(200, {
    query: '', total: 71, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 2, index_updated_at: BOARDS.index_updated_at, took_ms: 15, items: items(50, 'zoid'),
  });
  t.search({ board: 'zoid', year: '2015' });
  await tick(120);

  check('一覧が描画される', t.$('rows').children.length === 50, t.$('rows').children.length);
  check('見出しが一覧向けになる', t.$('scopeLabel').textContent.includes('スレッド一覧'), t.$('scopeLabel').textContent);
  check('「」の検索結果 になっていない', !t.$('scopeLabel').textContent.includes('「」'), t.$('scopeLabel').textContent);
  check('q を送っていない', !t.fetchLog.some((u) => u.includes('/api/search') && u.includes('q=')), t.fetchLog);
  check('JS エラーが出ていない', t.errors.length === 0, t.errors);
}

async function testBlockedYearIsRejectedLocally() {
  console.log('一覧不可の (板, 年) はサーバーに問い合わせない');
  const t = boot();
  await tick(50);
  t.search({ board: 'livejupiter', year: '2015' });   // BLOCKED に入っている
  await tick(120);

  check('検索リクエストが飛ばない', !t.fetchLog.some((u) => u.includes('/api/search')), t.fetchLog);
  check('案内が出る', t.$('notice').hidden === false && t.$('notice').textContent.includes('25万件'), t.$('notice').textContent);
  check('結果が残っていない', t.$('resultsCard').hidden === true);
}

// 年→月→日の絞り込み（2026-08-06 追加）
async function testPeriodCascade() {
  console.log('期間の絞り込み（年→月→日）');
  const t = boot();
  await tick(50);
  const $ = t.$;

  check('初期状態では月・日が隠れている', $('monthBox').hidden === true && $('dayBox').hidden === true);

  $('year').value = '2015'; $('year').dispatchEvent(new t.w.Event('change'));
  check('年を選ぶと月が出る', $('monthBox').hidden === false && $('dayBox').hidden === true);

  $('month').value = '02'; $('month').dispatchEvent(new t.w.Event('change'));
  check('月を選ぶと日が出る', $('dayBox').hidden === false);
  // 2015-02 は28日まで。存在しない日を選べてはいけない（サーバーに 400 を返させることになる）
  check('日数が月に合っている（2015-02 は28日）', $('day').options.length === 29, $('day').options.length);

  $('year').value = '2016'; $('year').dispatchEvent(new t.w.Event('change'));
  check('年を変えると月・日が捨てられる', $('month').value === '' && $('dayBox').hidden === true);
  $('month').value = '02'; $('month').dispatchEvent(new t.w.Event('change'));
  check('うるう年は29日まで（2016-02）', $('day').options.length === 30, $('day').options.length);

  // 粒度ごとに from/to が変わる
  const res = (total) => ({
    query: '', total, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 1, index_updated_at: BOARDS.index_updated_at, took_ms: 9, items: items(3, 'zoid'),
  });
  const lastSearch = () => t.fetchLog.filter((u) => u.includes('/api/search')).pop();

  t.setSearch(200, res(71));
  t.search({ board: 'zoid', year: '2015' });
  await tick(80);
  check('年だけ → from=2015-01&to=2015-12',
    /from=2015-01&/.test(lastSearch()) && /to=2015-12/.test(lastSearch()), lastSearch());

  t.search({ board: 'zoid', year: '2015', month: '03' });
  await tick(80);
  check('年月 → from=to=2015-03',
    /from=2015-03&/.test(lastSearch()) && /to=2015-03(&|$)/.test(lastSearch()), lastSearch());

  t.search({ board: 'zoid', year: '2015', month: '03', day: '05' });
  await tick(80);
  check('年月日 → from=to=2015-03-05',
    /from=2015-03-05&/.test(lastSearch()) && /to=2015-03-05(&|$)/.test(lastSearch()), lastSearch());
  check('見出しに日まで出る', t.$('scopeLabel').textContent.includes('2015年3月5日'), t.$('scopeLabel').textContent);
}

// 事前判定は選択の粒度に合わせて効く（年・月は判定、日は判定しない）
async function testBlockedGranularity() {
  console.log('事前判定の粒度');

  const blockedMonth = boot();
  await tick(50);
  blockedMonth.search({ board: 'livejupiter', year: '2017', month: '03' });   // blocked_months に該当
  await tick(80);
  check('一覧できない月は問い合わせない',
    !blockedMonth.fetchLog.some((u) => u.includes('/api/search')), blockedMonth.fetchLog);

  const okMonth = boot();
  await tick(50);
  okMonth.setSearch(200, {
    query: '', total: 1200, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 24, index_updated_at: BOARDS.index_updated_at, took_ms: 30, items: items(50, 'livejupiter'),
  });
  okMonth.search({ board: 'livejupiter', year: '2017', month: '04' });   // 年はブロックだが月は対象外
  await tick(80);
  check('同じ年でも対象外の月は問い合わせる',
    okMonth.fetchLog.some((u) => u.includes('/api/search')), okMonth.fetchLog);
  check('結果が描画される', okMonth.$('rows').children.length === 50, okMonth.$('rows').children.length);

  // 日まで指定したら、年・月がブロックでも必ず問い合わせる
  // （1日で25万件を超えることは有り得ないため。board-years.json も日は記録しない）
  const dayLevel = boot();
  await tick(50);
  dayLevel.setSearch(200, {
    query: '', total: 300, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 6, index_updated_at: BOARDS.index_updated_at, took_ms: 12, items: items(50, 'livejupiter'),
  });
  dayLevel.search({ board: 'livejupiter', year: '2017', month: '03', day: '05' });
  await tick(80);
  check('日まで指定すれば問い合わせる',
    dayLevel.fetchLog.some((u) => u.includes('/api/search')), dayLevel.fetchLog);
}

// 収集元に無い期間の断り書き（2026-08-21 追加。依頼者指示）
async function testDataGapNotice() {
  console.log('欠落期間のお知らせ');

  const t = boot();
  await tick(50);
  t.setSearch(200, {
    query: 'test', total: 0, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 0, index_updated_at: BOARDS.index_updated_at, took_ms: 8, items: [],
  });
  t.search({ q: 'test', board: 'livejupiter', year: '2013' });
  await tick(120);
  check('なんJ 2013年で出る',
    t.$('gapNotice').hidden === false && t.$('gapNotice').textContent.includes('なんJ'),
    t.$('gapNotice').textContent);

  // 年を外しても、その板を見ている限りは出す（0件の理由が伝わらないため）
  t.search({ q: 'test', board: 'news4vip' });
  await tick(120);
  check('VIP は年未指定でも出る',
    t.$('gapNotice').hidden === false && t.$('gapNotice').textContent.includes('ニュー速VIP'),
    t.$('gapNotice').textContent);

  // 板を選ばずに 2013年 を見ているときは、影響のある板を挙げるだけにする
  t.search({ q: 'test', year: '2013' });
  await tick(120);
  check('板未指定＋2013年でも出る',
    t.$('gapNotice').hidden === false && t.$('gapNotice').textContent.includes('2013年'),
    t.$('gapNotice').textContent);

  // 関係のない板・年では消える（前の検索の断り書きが残らないこと）
  t.search({ q: 'test', board: 'zoid', year: '2015' });
  await tick(120);
  check('無関係な条件では消える', t.$('gapNotice').hidden === true, t.$('gapNotice').textContent);
  check('JS エラーが出ていない', t.errors.length === 0, t.errors);
}

async function testNoAutoSearch() {
  console.log('条件を変えただけでは検索しない');
  const t = boot();
  await tick(50);
  const $ = t.$;
  $('board').value = 'zoid'; $('board').dispatchEvent(new t.w.Event('change'));
  $('year').value = '2015'; $('year').dispatchEvent(new t.w.Event('change'));
  $('sort').value = 'old'; $('sort').dispatchEvent(new t.w.Event('change'));
  await tick(120);
  check('検索リクエストが飛ばない', !t.fetchLog.some((u) => u.includes('/api/search')), t.fetchLog);
}

async function testCacheAvoidsRefetch() {
  console.log('同じ条件の再検索でリクエストが飛ばない');
  const t = boot();
  await tick(50);
  t.setSearch(200, {
    query: 'チーズ', total: 100, total_is_approximate: false, page: 1, per_page: 50,
    max_page: 2, index_updated_at: BOARDS.index_updated_at, took_ms: 42, items: items(50, 'zoid'),
  });
  t.search({ q: 'チーズ' });
  await tick(120);
  const n = t.fetchLog.filter((u) => u.includes('/api/search')).length;
  t.search({ q: 'チーズ' });
  await tick(120);
  const m = t.fetchLog.filter((u) => u.includes('/api/search')).length;
  check('2回目はリクエストしない', m === n, { '1回目': n, '2回目': m });
  check('結果は表示されたまま', t.$('rows').children.length === 50);
}

// URL からの復元。★ 既存の共有リンク（?q=..&from=YYYY-01&to=YYYY-12）が壊れないこと。
// この互換を落とすと、公開済みのブックマークやSNSに貼られたリンクが全て期間なしで開く。
async function testURLRestore() {
  console.log('URL からの期間の復元');
  const cases = [
    { url: '?q=%E3%83%81%E3%83%BC%E3%82%BA&board=zoid&from=2015-01&to=2015-12', y: '2015', m: '', d: '', label: '年（既存の共有リンク形式）' },
    { url: '?board=zoid&from=2015-03&to=2015-03', y: '2015', m: '03', d: '', label: '月' },
    { url: '?board=zoid&from=2015-03-05&to=2015-03-05', y: '2015', m: '03', d: '05', label: '日' },
    { url: '?q=%E3%83%81%E3%83%BC%E3%82%BA', y: '', m: '', d: '', label: '期間なし' },
  ];
  for (const c of cases) {
    const t = boot('http://localhost:8088/' + c.url);
    t.setSearch(200, {
      query: '', total: 5, total_is_approximate: false, page: 1, per_page: 50,
      max_page: 1, index_updated_at: BOARDS.index_updated_at, took_ms: 5, items: items(5, 'zoid'),
    });
    await tick(120);
    const got = { y: t.$('year').value, m: t.$('month').value, d: t.$('day').value };
    check(c.label + ' が復元される', got.y === c.y && got.m === c.m && got.d === c.d, got);
  }
}

async function testApiErrorIsShown() {
  console.log('API のエラーがそのまま案内として出る');
  const t = boot();
  await tick(50);
  t.setSearch(400, { error: { code: 'too_many_results', message: '該当が多すぎます（25万件以上）。' } });
  t.search({ q: 'の', board: 'zoid' });
  await tick(120);
  check('案内が出る', t.$('notice').hidden === false && t.$('notice').textContent.includes('多すぎます'), t.$('notice').textContent);
  check('スピナーが消えている', t.$('loadingCard').hidden === true);
}

(async () => {
  console.log('フロントエンド スモークテスト: ' + SRC + '\n');
  await testKeywordSearch();
  await testBrowse();
  await testBlockedYearIsRejectedLocally();
  await testPeriodCascade();
  await testBlockedGranularity();
  await testDataGapNotice();
  await testNoAutoSearch();
  await testURLRestore();
  await testCacheAvoidsRefetch();
  await testApiErrorIsShown();
  console.log('');
  if (failed) {
    console.log('★ ' + failed + ' 件失敗');
    process.exit(1);
  }
  console.log('全て通過');
})();
