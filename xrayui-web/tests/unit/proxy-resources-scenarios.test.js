'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');
const OPERATION_ID = '0123456789abcdef0123456789abcdef';

function appFixture(fetchImpl) {
  let factory;
  const window = {
    location: { hash: '' },
    setTimeout: (callback, delay) => setTimeout(callback, delay),
    clearTimeout: id => clearTimeout(id),
    setInterval: () => 0,
    addEventListener: () => {}
  };
  vm.runInNewContext(APP_SOURCE, {
    window,
    document: { addEventListener: (_name, callback) => callback() },
    Alpine: { data: (_name, create) => { factory = create; } },
    AbortController,
    fetch: fetchImpl || (async () => { throw new Error('unexpected fetch'); })
  });
  const app = factory();
  app.status = { controlEnabled: true, generation: 1 };
  app.safeApplyKnown = true;
  return app;
}

function snapshot(records, overrides = {}) {
  return {
    permanentDomains: [...records],
    pendingAdditions: [],
    pendingDeletions: [],
    effectiveDomains: [...records],
    runtime: { state: 'active' },
    consolidationActive: false,
    ...overrides
  };
}

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

test('adding an IPv4 record uses the existing endpoint, updates the mixed list and reports it as applied', async () => {
  const app = appFixture();
  const calls = [];
  const updated = snapshot(['example.com', '192.0.2.1']);
  app.proxyDomains = snapshot(['example.com']);
  app.proxyDomainInput = ' 192.0.2.1 ';
  app.api = async (requestPath, options = {}) => {
    calls.push({ requestPath, options });
    return { domain: '192.0.2.1', applied: true, snapshot: updated };
  };

  await app.addProxyDomain();

  assert.equal(calls.length, 1);
  assert.equal(calls[0].requestPath, '/api/proxy-domains');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].options.body), { domain: '192.0.2.1' });
  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainInput, '');
  assert.match(app.proxyDomainMessage, /192\.0\.2\.1/);
  assert.match(app.proxyDomainMessage, /примен/);
  assert.equal(app.pendingAction, '');
});

test('deleting an IPv4 record uses the encoded existing endpoint and keeps a newly typed value', async () => {
  const app = appFixture();
  const calls = [];
  const updated = snapshot(['example.com']);
  app.proxyDomains = snapshot(['example.com', '192.0.2.1']);
  app.proxyDomainInput = 'next.example';
  app.api = async (requestPath, options = {}) => {
    calls.push({ requestPath, options });
    return { domain: '192.0.2.1', applied: true, snapshot: updated };
  };

  await app.deleteProxyDomain('192.0.2.1');

  assert.equal(calls.length, 1);
  assert.equal(calls[0].requestPath, '/api/proxy-domains/192.0.2.1');
  assert.equal(calls[0].options.method, 'DELETE');
  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainInput, 'next.example');
  assert.match(app.proxyDomainMessage, /192\.0\.2\.1/);
  assert.match(app.proxyDomainMessage, /удал/);
  assert.equal(app.pendingAction, '');
  assert.equal(app.proxyDomainPendingName, '');
});

test('the canonical value from the helper, not the typed spelling, is shown after addition', async () => {
  const app = appFixture();
  const calls = [];
  const updated = snapshot(['example.com']);
  app.proxyDomainInput = '  Example.COM  ';
  app.api = async (requestPath, options = {}) => {
    calls.push({ requestPath, options });
    return { domain: 'example.com', applied: true, snapshot: updated };
  };

  await app.addProxyDomain();

  assert.deepEqual(JSON.parse(calls[0].options.body), { domain: 'Example.COM' });
  assert.deepEqual(app.proxyDomains.effectiveDomains, ['example.com']);
  assert.match(app.proxyDomainMessage, /example\.com/);
  assert.doesNotMatch(app.proxyDomainMessage, /Example\.COM/);
});

test('applied=false for an addition reports saved desired state without claiming runtime application', async () => {
  const app = appFixture();
  const updated = snapshot(['192.0.2.1']);
  app.proxyDomainInput = '192.0.2.1';
  app.api = async () => ({ domain: '192.0.2.1', applied: false, snapshot: updated });

  await app.addProxyDomain();

  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainInput, '');
  assert.match(app.proxyDomainMessage, /сохран/);
  assert.match(app.proxyDomainMessage, /не примен/);
  assert.doesNotMatch(app.proxyDomainMessage, /и применён/);
});

test('applied=false for a deletion reports saved desired state without claiming runtime application', async () => {
  const app = appFixture();
  const updated = snapshot(['example.com']);
  app.proxyDomains = snapshot(['example.com', '192.0.2.1']);
  app.api = async () => ({ domain: '192.0.2.1', applied: false, snapshot: updated });

  await app.deleteProxyDomain('192.0.2.1');

  assert.deepEqual(app.proxyDomains, updated);
  assert.match(app.proxyDomainMessage, /192\.0\.2\.1/);
  assert.match(app.proxyDomainMessage, /не примен/);
  assert.doesNotMatch(app.proxyDomainMessage, /и применён/);
});

test('an uncertain delete result refreshes by GET and keeps a warning without repeating DELETE', async () => {
  const app = appFixture();
  const old = snapshot(['example.com', '192.0.2.1']);
  const refreshed = snapshot(['example.com']);
  app.proxyDomains = old;
  app.proxyDomainInput = 'next.example';
  const calls = [];
  const failure = Object.assign(new Error('runtime synchronization failed'), {
    code: 'domain_runtime_sync_failed',
    mutationMayHavePersisted: true
  });
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath });
    if (options.method === 'DELETE') throw failure;
    return refreshed;
  };

  await app.deleteProxyDomain('192.0.2.1');

  assert.deepEqual(calls, [
    { method: 'DELETE', requestPath: '/api/proxy-domains/192.0.2.1' },
    { method: 'GET', requestPath: '/api/proxy-domains' }
  ]);
  assert.deepEqual(app.proxyDomains, refreshed);
  assert.equal(app.proxyDomainInput, 'next.example');
  assert.match(app.proxyDomainMessage, /могло быть (сохранено|удалено)|результат.*неизвест|возможно.*сохран/i);
  assert.match(app.proxyDomainMessage, /runtime synchronization failed/);
  assert.doesNotMatch(app.proxyDomainMessage, /удалён из прокси-маршрутизации/);
  assert.equal(app.pendingAction, '');
  assert.equal(app.proxyDomainPendingName, '');
});

test('an uncertain delete warning survives a failed refresh and never repeats DELETE', async () => {
  const app = appFixture();
  const old = snapshot(['example.com', '192.0.2.1']);
  app.proxyDomains = old;
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath });
    if (options.method === 'DELETE') {
      throw Object.assign(new Error('runtime apply failed'), {
        code: 'domain_runtime_apply_failed',
        mutationMayHavePersisted: true
      });
    }
    throw new Error('list refresh failed');
  };

  await app.deleteProxyDomain('192.0.2.1');

  assert.deepEqual(calls, [
    { method: 'DELETE', requestPath: '/api/proxy-domains/192.0.2.1' },
    { method: 'GET', requestPath: '/api/proxy-domains' }
  ]);
  assert.deepEqual(app.proxyDomains, old);
  assert.match(app.proxyDomainMessage, /могло быть (сохранено|удалено)|результат.*неизвест|возможно.*сохран/i);
  assert.match(app.proxyDomainMessage, /runtime apply failed/);
  assert.doesNotMatch(app.proxyDomainMessage, /list refresh failed/);
  assert.equal(app.pendingAction, '');
  assert.equal(app.proxyDomainPendingName, '');
});

test('a value typed while an addition is pending is preserved across duplicate submit clicks', async () => {
  const app = appFixture();
  app.proxyDomainInput = 'first.example';
  const pending = deferred();
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ requestPath, options });
    return pending.promise;
  };

  const first = app.addProxyDomain();
  app.proxyDomainInput = 'next.example';
  await app.addProxyDomain();
  pending.resolve({ domain: 'first.example', applied: true, snapshot: snapshot(['first.example']) });
  await first;

  assert.equal(calls.length, 1);
  assert.deepEqual(JSON.parse(calls[0].options.body), { domain: 'first.example' });
  assert.equal(app.proxyDomainInput, 'next.example');
  assert.deepEqual(app.proxyDomains.effectiveDomains, ['first.example']);
  assert.match(app.proxyDomainMessage, /first\.example/);
  assert.equal(app.pendingAction, '');
});

test('a lost add response keeps the entered value and never sends a second POST', async () => {
  const app = appFixture();
  const old = snapshot(['old.example']);
  app.proxyDomains = old;
  app.proxyDomainInput = 'lost.example';
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath });
    if (options.method === 'POST') {
      throw Object.assign(new Error('connection lost after request'), { status: 0, code: 'network_error' });
    }
    return old;
  };

  await app.addProxyDomain();

  assert.equal(calls.filter(call => call.method === 'POST').length, 1);
  assert.equal(calls.filter(call => call.method === 'POST')[0].requestPath, '/api/proxy-domains');
  assert.equal(app.proxyDomainInput, 'lost.example');
  assert.ok(app.proxyDomainMessage);
  assert.doesNotMatch(app.proxyDomainMessage, /добавлен в прокси-маршрутизацию|и применён/);
  assert.deepEqual(app.proxyDomains, old);
  assert.equal(app.pendingAction, '');
});

test('a lost delete response keeps the last known list and never sends a second DELETE', async () => {
  const app = appFixture();
  const old = snapshot(['example.com', '192.0.2.1']);
  app.proxyDomains = old;
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath });
    if (options.method === 'DELETE') {
      throw Object.assign(new Error('connection lost after request'), { status: 0, code: 'network_error' });
    }
    return old;
  };

  await app.deleteProxyDomain('192.0.2.1');

  assert.equal(calls.filter(call => call.method === 'DELETE').length, 1);
  assert.equal(calls.filter(call => call.method === 'DELETE')[0].requestPath, '/api/proxy-domains/192.0.2.1');
  assert.deepEqual(app.proxyDomains, old);
  assert.ok(app.proxyDomainMessage);
  assert.doesNotMatch(app.proxyDomainMessage, /удалён из прокси-маршрутизации/);
  assert.equal(app.pendingAction, '');
  assert.equal(app.proxyDomainPendingName, '');
});

test('control, setup, safe-apply and active update gates block add and delete without changing the form', async () => {
  const gates = [
    { name: 'control disabled', apply: app => { app.status = { controlEnabled: false }; } },
    { name: 'setup mode', apply: app => { app.setupMode = true; } },
    { name: 'setup recovery', apply: app => { app.setupMode = true; app.setupRecoveryRequired = true; } },
    { name: 'safe apply pending', apply: app => { app.safeApply = { transactionId: 'tx', templateId: 'sysctl' }; } },
    { name: 'safe apply requires reboot', apply: app => { app.safeApply = { transactionId: 'tx', templateId: 'nftables', rebootRequired: true }; } },
    { name: 'safe apply recovery incomplete', apply: app => { app.safeApply = { transactionId: 'tx', templateId: 'nftables', recoveryRequired: true }; } },
    { name: 'update preparing', apply: app => { app.helperUpdate = { controlEnabled: true, operation: { id: OPERATION_ID, state: 'preparing' } }; } },
    { name: 'update applying', apply: app => { app.helperUpdate = { controlEnabled: true, operation: { id: OPERATION_ID, state: 'applying' } }; } }
  ];

  for (const gate of gates) {
    const app = appFixture();
    const initial = snapshot(['example.com', '192.0.2.1']);
    app.proxyDomains = initial;
    app.proxyDomainInput = 'draft.example';
    gate.apply(app);
    const mutations = [];
    app.api = async (requestPath, options = {}) => {
      mutations.push([options.method || 'GET', requestPath]);
      return initial;
    };

    assert.equal(app.canMutate(), false, gate.name);
    await app.addProxyDomain();
    await app.deleteProxyDomain('192.0.2.1');

    assert.deepEqual(mutations, [], gate.name);
    assert.equal(app.proxyDomainInput, 'draft.example', gate.name);
    assert.deepEqual(app.proxyDomains, initial, gate.name);
  }
});

test('U6 failed or interrupted update recovery never unlocks ordinary proxy mutations', async () => {
  for (const state of ['failed', 'interrupted']) {
    const app = appFixture();
    const initial = snapshot(['192.0.2.1']);
    app.proxyDomains = initial;
    app.proxyDomainInput = 'draft.example';
    app.helperUpdate = {
      controlEnabled: true,
      operation: {
        id: OPERATION_ID,
        state,
        targetVersion: 'helper-v0.2.0',
        snapshotReady: true,
        attempts: []
      }
    };
    const mutations = [];
    app.api = async (requestPath, options = {}) => {
      mutations.push([options.method || 'GET', requestPath]);
      return initial;
    };

    assert.equal(app.canRetryUpdate(), true, state);
    assert.equal(app.canRollbackUpdate(), true, state);
    assert.equal(app.canMutate(), false, state);
    assert.match(app.mutationBlockedReason(), /обновление/, state);

    await app.addProxyDomain();
    await app.deleteProxyDomain('192.0.2.1');

    assert.deepEqual(mutations, [], state);
    assert.equal(app.proxyDomainInput, 'draft.example', state);
    assert.deepEqual(app.proxyDomains, initial, state);
  }
});

test('a list refresh replaces the snapshot on success and keeps the last known list when helper is unavailable', async () => {
  const app = appFixture();
  const old = snapshot(['old.example']);
  const mixed = snapshot(['old.example', '192.0.2.1']);
  app.proxyDomains = old;
  app.api = async () => mixed;

  await app.loadProxyDomains();

  assert.deepEqual(app.proxyDomains, mixed);
  assert.equal(app.proxyDomainMessage, '');
  assert.equal(app.proxyDomainLoading, false);

  app.proxyDomains = old;
  app.api = async () => { throw new Error('helper unavailable'); };

  await app.loadProxyDomains();

  assert.deepEqual(app.proxyDomains, old);
  assert.match(app.proxyDomainMessage, /helper unavailable/);
  assert.equal(app.proxyDomainLoading, false);
});

test('cancelling a pending deletion restores the canonical record and keeps a newly typed value', async () => {
  const app = appFixture();
  const pendingDeletion = snapshot([], {
    permanentDomains: ['kept.example'],
    pendingDeletions: ['kept.example'],
    effectiveDomains: []
  });
  const restored = snapshot(['kept.example']);
  app.proxyDomains = pendingDeletion;
  app.proxyDomainInput = 'next.example';
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ requestPath, options });
    return { domain: 'kept.example', applied: true, snapshot: restored };
  };

  await app.addProxyDomain('kept.example');

  assert.equal(calls.length, 1);
  assert.deepEqual(JSON.parse(calls[0].options.body), { domain: 'kept.example' });
  assert.deepEqual(app.proxyDomains, restored);
  assert.equal(app.proxyDomainInput, 'next.example');
  assert.match(app.proxyDomainMessage, /kept\.example/);
});

test('a list read started before a mutation cannot replace its newer snapshot or message', async () => {
  const app = appFixture();
  const old = snapshot(['old.example']);
  const updated = snapshot(['old.example', '192.0.2.1']);
  const pendingRead = deferred();
  app.proxyDomains = old;
  app.proxyDomainInput = '192.0.2.1';
  app.api = async (_path, options = {}) => options.method === 'POST'
    ? { domain: '192.0.2.1', applied: true, snapshot: updated }
    : pendingRead.promise;
  const read = app.loadProxyDomains();
  await app.addProxyDomain();
  const message = app.proxyDomainMessage;
  pendingRead.resolve(old);
  await read;
  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainMessage, message);
  assert.equal(app.proxyDomainsStale, false);
});

test('an invalid successful mutation response is not shown as a successful change', async () => {
  const app = appFixture();
  const old = snapshot(['old.example']);
  app.proxyDomains = old;
  app.proxyDomainInput = '192.0.2.1';
  const calls = [];
  app.api = async (_path, options = {}) => {
    calls.push(options.method || 'GET');
    return options.method === 'POST' ? {} : old;
  };
  await app.addProxyDomain();
  assert.deepEqual(calls, ['POST', 'GET']);
  assert.deepEqual(app.proxyDomains, old);
  assert.equal(app.proxyDomainInput, '192.0.2.1');
  assert.match(app.proxyDomainMessage, /не подтверждён/);
  assert.doesNotMatch(app.proxyDomainMessage, /добавлена.*применена/);
});

test('invalid list JSON keeps the previous snapshot and visibly marks it stale', async () => {
  const app = appFixture();
  const old = snapshot(['192.0.2.1']);
  app.proxyDomains = old;
  app.api = async () => ({});
  await app.loadProxyDomains();
  assert.deepEqual(app.proxyDomains, old);
  assert.equal(app.proxyDomainsStale, true);
  assert.match(app.proxyDomainMessage, /Некорректный ответ/);
});
