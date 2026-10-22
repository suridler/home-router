'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');
const HTML_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');

function appFixture(options = {}) {
  let factory;
  const document = {
    addEventListener: (_name, callback) => callback(),
    hidden: false,
    visibilityState: 'visible',
    ...(options.document || {})
  };
  const window = {
    location: { hash: '' },
    setTimeout: (callback, delay) => setTimeout(callback, delay),
    clearTimeout: id => clearTimeout(id),
    setInterval: () => 0,
    addEventListener: () => {},
    ...(options.window || {})
  };
  vm.runInNewContext(APP_SOURCE, {
    window,
    document,
    Alpine: { data: (_name, create) => { factory = create; } },
    AbortController,
    fetch: options.fetchImpl || (async () => { throw new Error('unexpected fetch'); })
  });
  return factory();
}

test('home events classify explicit levels, fatal/panic and journal text without treating failed as an error level', () => {
  const app = appFixture();

  assert.equal(app.logLineSeverity('2026/10/18 10:00:00 [Error] upstream unavailable'), 'error');
  assert.equal(app.logLineSeverity('2026/10/18 10:00:00 [Warning] upstream timed out'), 'warning');
  assert.equal(app.logLineSeverity('2026/10/18 10:00:00 [Info] connection failed; retrying'), 'info');
  assert.equal(app.logLineSeverity('fatal: cannot bind listener'), 'error');
  assert.equal(app.logLineSeverity('panic: runtime error'), 'error');
  assert.equal(app.logLineSeverity('2026-10-18T10:00:00+0300 router xray[42]: 2026/10/18 10:00:00 [Warning] journal warning'), 'warning');
  assert.equal(app.logLineSeverity('2026-10-18T10:00:00+0300 router xray[42]: failed to connect'), 'info');
  assert.equal(app.logLineSeverity('level=error reason=configuration'), 'error');
  assert.equal(app.errorTime('2026-10-18T10:00:00+0300 router xray[42]: [Info] ready'), '2026-10-18T10:00:00+0300');
});

test('home events filter by severity and show the last eight lines newest first', () => {
  const app = appFixture();
  const lines = Array.from({ length: 10 }, (_, index) => `2026/10/18 10:00:0${index} [Info] line ${index + 1}`);
  lines[2] = '2026/10/18 10:00:02 [Warning] line 3';
  lines[4] = '2026/10/18 10:00:04 [Error] line 5';
  app.homeEventLines = lines;
  app.homeEventFilter = 'all';

  assert.deepEqual(app.filteredHomeEvents(), lines.slice(-8).reverse());

  app.homeEventFilter = 'error';
  assert.deepEqual(app.filteredHomeEvents(), [lines[4]]);

  app.homeEventFilter = 'warning';
  assert.deepEqual(app.filteredHomeEvents(), [lines[2]]);

  app.homeEventFilter = 'info';
  assert.equal(app.filteredHomeEvents().length, 8);
  assert.ok(app.filteredHomeEvents().every(line => app.logLineSeverity(line) === 'info'));
  assert.equal(app.filteredHomeEvents()[0], lines[9]);
});

test('automatic refresh skips background, hidden and setup states', async () => {
  let requests = 0;
  const track = async () => { requests += 1; return { lines: [] }; };

  const background = appFixture();
  background.homeEventFilter = 'all';
  background.activeView = 'logs';
  background.api = track;
  await background.refreshHomeEvents();

  const setup = appFixture();
  setup.homeEventFilter = 'all';
  setup.activeView = 'status';
  setup.setupMode = true;
  setup.api = track;
  await setup.refreshHomeEvents();

  const hidden = appFixture({ document: { hidden: true } });
  hidden.homeEventFilter = 'all';
  hidden.activeView = 'status';
  hidden.api = track;
  await hidden.refreshHomeEvents();

  const hiddenVisibility = appFixture({ document: { visibilityState: 'hidden' } });
  hiddenVisibility.homeEventFilter = 'all';
  hiddenVisibility.activeView = 'status';
  hiddenVisibility.api = track;
  await hiddenVisibility.refreshHomeEvents();

  assert.equal(requests, 0);

  const active = appFixture();
  active.homeEventFilter = 'all';
  active.activeView = 'status';
  active.api = track;
  await active.refreshHomeEvents();
  assert.equal(requests, 1);
});

test('automatic refresh does not overlap an in-flight request', async () => {
  const app = appFixture();
  app.homeEventFilter = 'all';
  app.activeView = 'status';
  let requests = 0;
  let resolveRequest;
  app.api = () => {
    requests += 1;
    return new Promise(resolve => { resolveRequest = resolve; });
  };

  const first = app.refreshHomeEvents();
  const second = app.refreshHomeEvents();
  assert.equal(requests, 1);
  assert.equal(app.homeEventLoading, true);

  resolveRequest({ lines: ['2026/10/18 10:00:00 [Info] ready'] });
  await Promise.all([first, second]);

  assert.equal(requests, 1);
  assert.equal(app.homeEventLoading, false);
  assert.deepEqual(app.homeEventLines, ['2026/10/18 10:00:00 [Info] ready']);
});

test('failed refresh retains previous events and timestamp and marks them stale', async () => {
  const app = appFixture();
  app.api = async () => ({ lines: ['2026/10/18 10:00:00 [Warning] previous event'] });
  await app.loadHomeEvents();
  const updatedAt = app.homeEventUpdatedAt;
  assert.ok(updatedAt);
  assert.equal(app.homeEventStale, false);
  assert.equal(app.homeEventError, '');

  app.api = async () => { throw new Error('helper unavailable'); };
  await app.loadHomeEvents();

  assert.deepEqual(app.homeEventLines, ['2026/10/18 10:00:00 [Warning] previous event']);
  assert.equal(app.homeEventUpdatedAt, updatedAt);
  assert.equal(app.homeEventError, 'helper unavailable');
  assert.equal(app.homeEventStale, true);
  assert.match(app.homeEventStatus(), /helper unavailable/);
  assert.match(app.homeEventStatus(), /Показаны данные на/);
});

test('full log viewer requests the helper-supported limit of 200 lines', async () => {
  const app = appFixture();
  const calls = [];
  app.api = async requestPath => {
    calls.push(requestPath);
    return { lines: ['log line'] };
  };

  app.selectedLogKind = 'error';
  await app.loadLogs();
  app.selectedLogKind = 'access';
  await app.loadLogs();

  app.selectedLogKind = 'helper';
  await app.loadLogs();
  assert.deepEqual(calls, ['/api/logs/error?limit=200', '/api/logs/access?limit=200', '/api/logs/helper?limit=200']);
  assert.match(HTML_SOURCE, /selectedLogKind === 'helper'/);
  assert.deepEqual(Array.from(app.logs), ['log line']);
});

test('homepage events use text-only rendering and position-stable keys', () => {
  assert.match(HTML_SOURCE, /Журнал Xray/);
  assert.match(HTML_SOURCE, /@click="selectHomeEventFilter\('all'\)">Все/);
  assert.match(HTML_SOURCE, /@click="selectHomeEventFilter\('error'\)">Ошибки/);
  assert.match(HTML_SOURCE, /@click="selectHomeEventFilter\('warning'\)">Предупреждения/);
  assert.match(HTML_SOURCE, /@click="selectHomeEventFilter\('info'\)">Информация/);
  assert.match(HTML_SOURCE, /x-for="\(line, index\) in filteredHomeEvents\(\)" :key="index"/);
  assert.match(HTML_SOURCE, /<code x-text="line"><\/code>/);
  assert.doesNotMatch(HTML_SOURCE, /x-html/);
  assert.doesNotMatch(HTML_SOURCE, /status\?\.recentErrors/);
});


test('known info priority and ordinary panic text do not masquerade as errors', () => {
  const app = appFixture();
  assert.equal(app.logLineSeverity('<6>panic: quoted diagnostic'), 'info');
  assert.equal(app.logLineSeverity('connection accepted to panic.example.com'), 'info');
  assert.equal(app.logLineSeverity('<4>warning message'), 'warning');
  assert.equal(app.logLineSeverity('<3>failure'), 'error');
  assert.equal(app.logLineSeverity('router xray[42]: panic: runtime error'), 'error');
});

test('initial and periodic event fetch respect active view and visibility', async () => {
  for (const [hash, hidden, expected] of [['#status', false, 1], ['#routing', false, 0], ['#status', true, 0]]) {
    const timers = [];
    const app = appFixture({ document: { hidden }, window: { location: { hash }, setInterval: (callback, delay) => { timers.push({ callback, delay }); } } });
    app.homeEventFilter = 'all';
    app.loadSetup = async () => false;
    for (const method of ['loadStatus', 'loadLogs', 'loadGeodata', 'loadProxyDomains', 'loadTemplates', 'loadSystemSettings', 'loadSafeApply']) app[method] = async () => {};
    let calls = 0;
    app.api = async requestPath => {
      assert.equal(requestPath, '/api/logs/error?limit=100');
      calls++;
      return { lines: [] };
    };
    await app.init();
    assert.equal(calls, expected);
    const poll = timers.find(timer => timer.delay === 10000);
    assert.ok(poll);
    await poll.callback();
    assert.equal(calls, expected * 2);
  }
});

test('combined journal defaults to durable errors and fetches live events only when selected', async () => {
  const app = appFixture();let reads = 0;
  app.api = async () => { reads++; return {lines:['[Info] live event']}; };
  assert.equal(app.homeEventFilter,'error');
  await app.refreshHomeEvents();assert.equal(reads,0);
  await app.selectHomeEventFilter('all');assert.equal(reads,1);assert.equal(app.homeEventLines[0],'[Info] live event');
  await app.selectHomeEventFilter('error');await app.refreshHomeEvents();assert.equal(reads,1);
  await app.selectHomeEventFilter('invalid');assert.equal(app.homeEventFilter,'error');
  assert.equal((HTML_SOURCE.match(/aria-labelledby="home-events-title"/g)||[]).length,1);
  assert.doesNotMatch(HTML_SOURCE,/aria-labelledby="error-history-title"/);
});


test('full logs show newest first without mutating the API response', async () => {
  const app = appFixture();
  const lines = ['old', 'middle', 'new'];
  app.api = async () => ({ lines });
  for (const kind of ['error', 'access', 'helper']) {
    app.selectedLogKind = kind;
    await app.loadLogs();
    assert.deepEqual(Array.from(app.logs), ['new', 'middle', 'old']);
  }
  assert.deepEqual(lines, ['old', 'middle', 'new']);
});

test('automatic log refresh only polls an enabled visible log view and never overlaps', async () => {
  const page = { hidden: false };
  const app = appFixture({ document: page });
  let calls = 0;
  let resolve;
  app.api = async () => { calls++; return new Promise(done => { resolve = done; }); };
  app.activeView = 'logs';
  await app.refreshLogs();
  assert.equal(calls, 0);
  app.logAutoRefresh = true;
  app.activeView = 'status';
  await app.refreshLogs();
  assert.equal(calls, 0);
  app.activeView = 'logs';
  app.setupMode = true;
  await app.refreshLogs();
  assert.equal(calls, 0);
  app.setupMode = false;
  const poll = app.refreshLogs();
  await app.refreshLogs();
  assert.equal(calls, 1);
  resolve({ lines: ['event'] });
  await poll;
  const hiddenApp = appFixture({ document: { hidden: true } });
  hiddenApp.logAutoRefresh = true;
  hiddenApp.activeView = 'logs';
  hiddenApp.api = () => assert.fail('hidden tab polled logs');
  await hiddenApp.refreshLogs();
});

test('initialization schedules log refresh every ten seconds', async () => {
  const intervals = [];
  const app = appFixture({ window: { setInterval: (fn, ms) => { intervals.push({fn, ms}); return 0; } } });
  for (const name of ['syncNavigation', 'startUpdatePolling', 'loadUpdateStatus', 'loadSetup', 'loadStatus', 'loadLogs', 'loadGeodata', 'loadProxyDomains', 'loadTemplates', 'loadSystemSettings', 'loadSafeApply', 'refreshHomeEvents', 'loadNetworkProbes', 'loadConfigRollback']) app[name] = async () => false;
  app.updateInitialCheckStarted = true;
  let refreshed = 0;
  app.refreshLogs = () => { refreshed++; };
  await app.init();
  const poll = intervals.find(item => item.ms === 10000);
  assert.ok(poll);
  poll.fn();
  assert.equal(refreshed, 1);
  assert.match(HTML_SOURCE, /x-model="logAutoRefresh"/);
});
