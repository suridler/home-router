'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');
const HTML_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');
const OP = '0123456789abcdef0123456789abcdef';
const ACTION = 'fedcba9876543210fedcba9876543210';

function memoryStorage() {
  const values = new Map();
  return {
    getItem: key => values.has(key) ? values.get(key) : null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: key => values.delete(key)
  };
}

function appFixture(options = {}) {
  let factory;
  const timers = [];
  const storage = options.storage === undefined ? memoryStorage() : options.storage;
  const window = {
    location: { hash: '' },
    confirm: options.confirm || (() => true),
    setTimeout: (callback, delay) => setTimeout(callback, delay),
    clearTimeout: id => clearTimeout(id),
    setInterval: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
    addEventListener: () => {},
    ...(storage ? { sessionStorage: storage } : {}),
    ...(options.window || {})
  };
  vm.runInNewContext(APP_SOURCE, {
    window,
    document: { addEventListener: (_name, callback) => callback() },
    Alpine: { data: (_name, create) => { factory = create; } },
    AbortController,
    crypto: require('node:crypto').webcrypto,
    fetch: options.fetchImpl || (async () => { throw new Error('unexpected fetch'); })
  });
  const app = factory();
  app.status = { controlEnabled: true, generation: 1, logs: {} };
  app.safeApplyKnown = true;
  return { app, timers, storage };
}

test('update panel is standalone near the header and outside setup/recovery gating', () => {
  const panel = HTML_SOURCE.match(/<section class="panel full-width helper-update-panel"[\s\S]*?<\/section>/)?.[0];
  assert.ok(panel, 'update panel is present');
  assert.match(panel, /id="helper-update-title"/);
  assert.match(panel, /Проверить обновления/);
  assert.match(panel, /Установленная версия/);
  assert.match(panel, /Последняя версия GitHub/);
  assert.match(panel, /@click="updatePrimaryAction\(\)"/);
  assert.match(panel, /@click="retryUpdate\(\)"/);
  assert.match(panel, /@click="rollbackUpdate\(\)"/);
  assert.doesNotMatch(panel, /!setupMode/);
  assert.ok(HTML_SOURCE.indexOf('helper-update-panel') < HTML_SOURCE.indexOf('x-show="setupMode"'));
  assert.ok(HTML_SOURCE.indexOf('helper-update-panel') < HTML_SOURCE.indexOf('activeView === \'status\''));
  assert.match(APP_SOURCE, /\/api\/helper-update/);
  assert.match(APP_SOURCE, /window\.sessionStorage/);
  assert.match(APP_SOURCE, /randomUUID\(\)\.replaceAll\('-', ''\)/);
  assert.match(APP_SOURCE, /getRandomValues/);
  assert.match(APP_SOURCE, /setInterval\(\(\) => \{ this\.pollUpdateStatus\(\); \}, 3000\)/);
});

test('update action is offered only for a known newer version with no pending operation and enabled control', () => {
  const { app } = appFixture();
  app.helperUpdate = { controlEnabled: true, operation: null };
  app.updateCheck = { latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' };
  assert.equal(app.canPrepareUpdate(), true);

  app.updateCheck = { ...app.updateCheck, comparison: 'unknown' };
  assert.equal(app.canPrepareUpdate(), false);
  app.updateCheck = { latestVersion: 'helper-v0.2.0', updateAvailable: false, comparison: 'known' };
  assert.equal(app.canPrepareUpdate(), false);
  app.updateCheck = { latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' };
  app.helperUpdate = { controlEnabled: false, operation: null };
  assert.equal(app.canPrepareUpdate(), false);
  app.helperUpdate = { controlEnabled: true, operation: { id: OP, state: 'applying' } };
  assert.equal(app.canPrepareUpdate(), false);
  app.helperUpdate = { controlEnabled: true, operation: { id: OP, state: 'succeeded' } };
  assert.equal(app.canPrepareUpdate(), true);
});

test('prepared operation applies the same operation after a later check without re-preparing or replacing its version', async () => {
  const confirmations = [];
  const { app } = appFixture({ confirm: message => { confirmations.push(message); return true; } });
  app.helperUpdate = { controlEnabled: true, currentVersion: 'helper-v0.1.0', operation: null };
  app.updateCheck = { latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' };
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ path: requestPath, method: options.method, body: options.body ? JSON.parse(options.body) : null });
    if (requestPath === '/api/helper-update/prepare') {
      return { operation: { id: OP, state: 'prepared', targetVersion: 'helper-v0.2.0', stage: 'prepared', snapshotReady: false, attempts: [] } };
    }
    if (requestPath === '/api/helper-update/apply') {
      return { operation: { id: OP, state: 'launching', targetVersion: 'helper-v0.2.0', stage: 'launching', snapshotReady: true, attempts: [{ id: ACTION, action: 'apply', state: 'launching', stage: 'launching' }] } };
    }
    throw new Error(`unexpected request ${requestPath}`);
  };

  await app.prepareUpdate('helper-v0.2.0');
  assert.equal(confirmations.length, 1);
  assert.match(confirmations[0], /helper-v0\.2\.0/);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, '/api/helper-update/prepare');
  assert.deepEqual(calls[0].body, { version: 'helper-v0.2.0' });
  assert.equal(app.updateState(), 'prepared');

  // A later check must not silently substitute a new target or prepare again.
  app.updateCheck = { latestVersion: 'helper-v9.0.0', updateAvailable: true, comparison: 'known' };
  await app.updatePrimaryAction();
  assert.equal(confirmations.length, 2);
  assert.match(confirmations[1], /helper-v0\.2\.0/);
  assert.equal(calls.length, 2);
  assert.equal(calls[1].path, '/api/helper-update/apply');
  assert.equal(calls[1].body.operationId, OP);
  assert.match(calls[1].body.actionId, /^[a-f0-9]{32}$/);
  assert.equal(calls.some(call => call.path === '/api/helper-update/check'), false);
});

test('pending update states block ordinary mutations while prepared permits them', () => {
  for (const state of ['preparing', 'launching', 'applying', 'verifying', 'failed', 'interrupted']) {
    const { app } = appFixture();
    app.helperUpdate = { controlEnabled: true, operation: { id: OP, state } };
    assert.equal(app.updateBlocksMutations(), true, state);
    assert.equal(app.canMutate(), false, state);
    assert.match(app.mutationBlockedReason(), /обновление/);
  }
  for (const state of ['prepared', 'succeeded', 'rolled_back']) {
    const { app } = appFixture();
    app.helperUpdate = { controlEnabled: true, operation: { id: OP, state } };
    assert.equal(app.updateBlocksMutations(), false, state);
    assert.equal(app.canMutate(), true, state);
  }
});

test('failed or interrupted update exposes retry always and rollback only with a ready snapshot', () => {
  for (const state of ['failed', 'interrupted']) {
    const { app } = appFixture();
    app.helperUpdate = { controlEnabled: true, operation: { id: OP, state, targetVersion: 'helper-v0.2.0', snapshotReady: false } };
    assert.equal(app.canRetryUpdate(), true, state);
    assert.equal(app.canRollbackUpdate(), false, state);
    assert.match(app.updateRecoveryMessage(), /снимок/i);

    app.helperUpdate.operation.snapshotReady = true;
    assert.equal(app.canRetryUpdate(), true, state);
    assert.equal(app.canRollbackUpdate(), true, state);
    assert.match(app.updateRecoveryMessage(), /повторить|откатить/i);
  }
});

test('a lost apply response performs only a status read and never automatically replays the mutation', async () => {
  const { app, storage } = appFixture();
  app.helperUpdate = {
    controlEnabled: true,
    currentVersion: 'helper-v0.1.0',
    operation: { id: OP, state: 'prepared', targetVersion: 'helper-v0.2.0', stage: 'prepared', snapshotReady: false, attempts: [] }
  };
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ path: requestPath, method: options.method });
    if (requestPath === '/api/helper-update/apply') throw new Error('connection lost');
    return {
      controlEnabled: true,
      currentVersion: 'helper-v0.1.0',
      operation: { id: OP, state: 'launching', targetVersion: 'helper-v0.2.0', stage: 'launching', snapshotReady: true, attempts: [] }
    };
  };

  await app.applyUpdate();
  assert.equal(calls.filter(call => call.method === 'POST').length, 1);
  assert.equal(calls[0].path, '/api/helper-update/apply');
  assert.equal(calls[1].path, '/api/helper-update');
  assert.equal(calls[1].method, undefined);
  assert.equal(calls.some(call => call.path === '/api/helper-update/prepare'), false);
  assert.equal(app.updatePolling, true);
  assert.match(app.updateError, /автоматически не повторяется/);
  assert.match(storage.getItem('xrayui.update.actionId'), /^[a-f0-9]{32}$/);
  assert.equal(storage.getItem('xrayui.update.operationId'), OP);
});

test('the three-second poll performs only GET status reads and does not check GitHub', async () => {
  const { app, timers } = appFixture();
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ path: requestPath, method: options.method });
    return {
      controlEnabled: true,
      currentVersion: 'helper-v0.1.0',
      operation: { id: OP, state: 'applying', targetVersion: 'helper-v0.2.0', stage: 'stop_services', snapshotReady: true, attempts: [] }
    };
  };
  app.updatePolling = true;
  app.startUpdatePolling();
  const poll = timers.find(timer => timer.delay === 3000);
  assert.ok(poll);
  await poll.callback();
  assert.deepEqual(calls, [{ path: '/api/helper-update', method: undefined }]);
});

test('startup loads status once and performs at most one initial GitHub check', async () => {
  const { app, timers } = appFixture();
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ path: requestPath, method: options.method });
    if (requestPath === '/api/helper-update') return { controlEnabled: true, currentVersion: 'helper-v0.1.0', currentCommit: 'abc', operation: null };
    if (requestPath === '/api/helper-update/check') return { currentVersion: 'helper-v0.1.0', latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' };
    throw new Error(`unexpected request ${requestPath}`);
  };
  app.loadSetup = async () => false;
  app.loadStatus = async () => {};
  app.loadLogs = async () => {};
  app.loadGeodata = async () => {};
  app.loadProxyDomains = async () => {};
  app.loadTemplates = async () => {};
  app.loadSystemSettings = async () => {};
  app.loadSafeApply = async () => {};
  app.loadNetworkProbes = async () => {};
  app.refreshHomeEvents = async () => {};

  await app.init();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(calls.filter(call => call.path === '/api/helper-update' && call.method === undefined).length, 1);
  assert.equal(calls.filter(call => call.path === '/api/helper-update/check').length, 1);

  const poll = timers.find(timer => timer.delay === 3000);
  assert.ok(poll);
  const before = calls.length;
  await poll.callback();
  assert.equal(calls.length, before, 'no GitHub check or helper mutation during idle polling');
});

test('a page reload during helper/web restart keeps polling persisted operation IDs without replaying work', async () => {
  const { app, storage } = appFixture();
  storage.setItem('xrayui.update.operationId', OP);
  storage.setItem('xrayui.update.actionId', ACTION);
  storage.setItem('xrayui.update.action', 'apply');
  storage.setItem('xrayui.update.targetVersion', 'helper-v0.2.0');
  storage.setItem('xrayui.update.uncertain', '1');
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ path: requestPath, method: options.method });
    throw new Error('helper is restarting');
  };

  await app.loadUpdateStatus();
  assert.match(app.updateReadError, /restarting/);
  assert.equal(app.updatePolling, true);
  assert.equal(app.updateUncertain, true);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, '/api/helper-update');
  assert.equal(calls.some(call => call.method === 'POST'), false);
});

test('action IDs use 32 lowercase hexadecimal characters from crypto', () => {
  const { app } = appFixture();
  const actionId = app.newUpdateActionId();
  assert.match(actionId, /^[a-f0-9]{32}$/);
  assert.notEqual(app.newUpdateActionId(), actionId);
});

test('sessionStorage failure is safely ignored and never blocks the panel', () => {
  const throwing = {
    getItem() { throw new Error('blocked'); },
    setItem() { throw new Error('blocked'); },
    removeItem() { throw new Error('blocked'); }
  };
  const { app } = appFixture({ storage: throwing });
  assert.equal(app.updateStorageRead('xrayui.update.operationId'), '');
  assert.doesNotThrow(() => app.updateStorageWrite('xrayui.update.operationId', OP));
  assert.doesNotThrow(() => app.reconcileUpdateStorage({ id: OP, state: 'prepared', targetVersion: 'helper-v0.2.0', attempts: [] }));
});
