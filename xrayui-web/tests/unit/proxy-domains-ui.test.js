'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');

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
  const app = factory(); app.safeApplyKnown = true; return app;
}

function snapshot(...domains) {
  return {
    permanentDomains: domains,
    pendingAdditions: [],
    pendingDeletions: [],
    effectiveDomains: domains,
    runtime: { state: 'active' },
    consolidationActive: false
  };
}

test('deletion messages distinguish USERLIST deferral from immediate dynamic removal', () => {
  const app = appFixture();
  const pending = { ...snapshot(), pendingDeletions: ['example.com'] };
  assert.match(app.proxyDomainDeletedMessage({ applied: true, snapshot: pending }, 'example.com'),
    /останется в USERLIST до следующего обновления геобаз/);
  assert.match(app.proxyDomainDeletedMessage({ applied: true, snapshot: snapshot() }, 'example.com'),
    /удалена из динамического списка/);
  assert.match(app.proxyDomainDeletedMessage({ applied: false, snapshot: pending }, 'example.com'),
    /ещё не применены/);
});

test('proxy domain additions are gated in the handler for disabled control, pending work, safe apply and setup', async () => {
  const blockedStates = [
    { status: { controlEnabled: false } },
    { status: { controlEnabled: true }, pendingAction: 'another-operation' },
    { status: { controlEnabled: true }, safeApply: { rebootRequired: true } },
    { status: { controlEnabled: true }, safeApply: { recoveryRequired: true } },
    { status: { controlEnabled: true }, setupMode: true }
  ];
  for (const blocked of blockedStates) {
    const app = appFixture();
    Object.assign(app, blocked);
    app.proxyDomainInput = 'example.com';
    let requests = 0;
    app.api = async () => { requests += 1; return {}; };

    await app.addProxyDomain();
    await app.addProxyDomain('undo.example');

    assert.equal(requests, 0, JSON.stringify(blocked));
    assert.equal(app.proxyDomainInput, 'example.com');
  }
});

test('whitespace-only domain input makes no POST request', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomainInput = ' \t \n ';
  let requests = 0;
  app.api = async () => { requests += 1; return {}; };

  await app.addProxyDomain();

  assert.equal(requests, 0);
  assert.equal(app.proxyDomainInput, ' \t \n ');
});

test('successful addition posts trimmed domain, updates the snapshot and clears only the form input', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  const updated = snapshot('example.com');
  const calls = [];
  app.api = async (requestPath, options) => {
    calls.push({ requestPath, options });
    return { domain: 'example.com', applied: true, snapshot: updated };
  };
  app.proxyDomainInput = '  example.com  ';

  await app.addProxyDomain();

  assert.equal(calls.length, 1);
  assert.equal(calls[0].requestPath, '/api/proxy-domains');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].options.body), { domain: 'example.com' });
  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainInput, '');
  assert.match(app.proxyDomainMessage, /добавлена в прокси-маршрутизацию и применена/);
  assert.equal(app.proxyDomainsStale, false);
  assert.equal(app.pendingAction, '');
});

test('applied=false reports saved state without claiming runtime success', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  const updated = snapshot('saved.example');
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push([options.method || 'GET', requestPath]);
    return options.method === 'POST'
      ? { domain: 'saved.example', applied: false, snapshot: updated }
      : updated;
  };
  app.proxyDomainInput = 'saved.example';

  await app.addProxyDomain();

  assert.deepEqual(calls, [
    ['POST', '/api/proxy-domains']
  ]);
  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainsStale, false);
  assert.equal(app.proxyDomainInput, '');
  assert.match(app.proxyDomainMessage, /сохранена в списке/);
  assert.match(app.proxyDomainMessage, /ещё не применены/);
  assert.doesNotMatch(app.proxyDomainMessage, /и применена/);
});

test('undo-delete addition keeps the shared form input intact', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  const updated = snapshot('kept.example');
  app.api = async () => ({ domain: 'kept.example', applied: true, snapshot: updated });
  app.proxyDomainInput = 'typed-but-not-submitted.example';

  await app.addProxyDomain('kept.example');

  assert.deepEqual(app.proxyDomains, updated);
  assert.equal(app.proxyDomainInput, 'typed-but-not-submitted.example');
  assert.match(app.proxyDomainMessage, /kept\.example/);
});

test('mutationMayHavePersisted failure refreshes via GET and keeps warning and input on refresh success', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomains = snapshot('old.example');
  app.proxyDomainInput = 'new.example';
  const refreshed = snapshot('old.example', 'new.example');
  const calls = [];
  const failure = Object.assign(new Error('runtime sync failed'), {
    code: 'domain_runtime_sync_failed',
    mutationMayHavePersisted: true
  });
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath, options });
    if (options.method === 'POST') throw failure;
    return refreshed;
  };

  await app.addProxyDomain();

  assert.deepEqual(calls.map(call => [call.method, call.requestPath]), [
    ['POST', '/api/proxy-domains'],
    ['GET', '/api/proxy-domains']
  ]);
  assert.deepEqual(app.proxyDomains, refreshed);
  assert.equal(app.proxyDomainsStale, false);
  assert.equal(app.proxyDomainInput, 'new.example');
  assert.match(app.proxyDomainMessage, /могло быть сохранено/);
  assert.match(app.proxyDomainMessage, /runtime sync failed/);
  assert.equal(app.pendingAction, '');
});

test('mutationMayHavePersisted warning survives a failed list refresh without a second POST', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomains = snapshot('old.example');
  app.proxyDomainInput = 'uncertain.example';
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push({ method: options.method || 'GET', requestPath, options });
    if (options.method === 'POST') {
      throw Object.assign(new Error('runtime apply failed'), {
        code: 'domain_runtime_apply_failed',
        mutationMayHavePersisted: true
      });
    }
    throw new Error('list refresh failed');
  };

  await app.addProxyDomain();

  assert.deepEqual(calls.map(call => [call.method, call.requestPath]), [
    ['POST', '/api/proxy-domains'],
    ['GET', '/api/proxy-domains']
  ]);
  assert.deepEqual(app.proxyDomains, snapshot('old.example'));
  assert.equal(app.proxyDomainsStale, true);
  assert.equal(app.proxyDomainInput, 'uncertain.example');
  assert.match(app.proxyDomainMessage, /могло быть сохранено/);
  assert.match(app.proxyDomainMessage, /runtime apply failed/);
  assert.doesNotMatch(app.proxyDomainMessage, /list refresh failed/);
  assert.equal(app.pendingAction, '');
});

test('timeout failure does not duplicate the POST or clear the input', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomainInput = 'timeout.example';
  const calls = [];
  app.api = async (requestPath, options = {}) => {
    calls.push([options.method || 'GET', requestPath]);
    throw Object.assign(new Error('Request timed out.'), { name: 'AbortError', code: 'request_timeout', status: 0 });
  };

  await app.addProxyDomain();

  assert.deepEqual(calls, [
    ['POST', '/api/proxy-domains'],
    ['GET', '/api/proxy-domains']
  ]);
  assert.equal(app.proxyDomainsStale, true);
  assert.equal(app.proxyDomainInput, 'timeout.example');
  assert.match(app.proxyDomainMessage, /Request timed out/);
  assert.match(app.proxyDomainMessage, /могло быть сохранено/);
  assert.equal(app.pendingAction, '');
});

test('api preserves mutationMayHavePersisted from failed responses on the thrown error', async () => {
  for (const flag of [true, false]) {
    const app = appFixture(async () => ({
      ok: false,
      status: 502,
      headers: { get: () => 'application/json' },
      json: async () => ({
        message: 'runtime synchronization failed',
        code: 'domain_runtime_sync_failed',
        mutationMayHavePersisted: flag
      })
    }));

    await assert.rejects(
      app.api('/api/proxy-domains', { method: 'POST' }),
      error => error.status === 502
        && error.code === 'domain_runtime_sync_failed'
        && error.mutationMayHavePersisted === flag
    );
  }
});

test('status view has a compact accessible add form, routing link and real runtime-state badge', () => {
  const html = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');
  assert.match(html, /id="status-proxy-domain"[^>]*maxlength="253"/);
  assert.match(html, /@submit\.prevent="addProxyDomain\(\)"/);
  assert.match(html, /role="status" aria-live="polite"/);
  assert.match(html, /href="#routing" @click\.prevent="navigate\('routing'\)"/);
  assert.match(html, /status\?\.proxyState === 'direct-placeholder'/);
  assert.match(html, /Домен или IPv4/);
  assert.match(html, /example\.com или 192\.0\.2\.1/);
  assert.match(html, /Домены и IPv4/);
  assert.match(html, /Добавить/);
  assert.match(html, /proxyDomainsStale/);
  assert.match(html, /Удалить \$\{domain\} из прокси-маршрутизации/);
  assert.match(html, /proxyDomains\?\.runtime\?\.state/);
  assert.match(html, /proxyDomains\?\.consolidationActive/);
  assert.doesNotMatch(html, /runtime\?\.api/);
});


test('pending addition prevents duplicate POST and preserves input changed during request', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomainInput = 'first.example';
  let complete;
  let calls = 0;
  app.api = async () => {
    calls++;
    return new Promise(resolve => { complete = resolve; });
  };
  const pending = app.addProxyDomain();
  app.proxyDomainInput = 'next.example';
  await app.addProxyDomain();
  assert.equal(calls, 1);
  complete({ domain: 'first.example', applied: true, snapshot: snapshot('first.example') });
  await pending;
  assert.equal(app.proxyDomainInput, 'next.example');
  assert.equal(app.pendingAction, '');
});

test('geodata update returns after admission and polling refreshes resources on completion', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.proxyDomains = snapshot('old.example');
  const operation = { id: 'async-operation', state: 'running' };
  let state = 'running';
  let starts = 0;
  let resourceReads = 0;
  app.loadStatus = async () => {};
  app.loadProxyDomains = async () => { resourceReads++; };
  app.api = async (url, options = {}) => {
    if (url === '/api/geodata/update') {
      starts++;
      assert.equal(options.timeoutMs, undefined);
      return { operation };
    }
    if (url === '/api/settings/geodata') return { operation: { ...operation, state }, settings: {}, progressLabel: 'Внешние базы · Скачивание' };
    throw new Error(`unexpected ${url}`);
  };
  await app.updateGeodata();
  assert.equal(starts, 1);
  assert.equal(app.pendingAction, '');
  assert.equal(app.geodata.operation.state, 'running');
  assert.match(app.geodataMessage, /Скачивание/);
  await app.updateGeodata();
  assert.equal(starts, 1);
  state = 'succeeded';
  await app.loadGeodata();
  assert.equal(resourceReads, 1);
  assert.equal(app.geodataMessage, '');
});

test('reopened page observes a running geodata update and later shows its failure stage', async () => {
  const app = appFixture();
  let state = 'running';
  app.loadStatus = async () => {};
  app.loadProxyDomains = async () => {};
  app.api = async url => {
    assert.equal(url, '/api/settings/geodata');
    return { operation: { id: 'restored-operation', state }, settings: {},
      progressLabel: 'Внешние базы · Скачивание', failureLabel: 'Контрольная сумма · geosite.dat: не совпадает' };
  };
  await app.loadGeodata();
  assert.match(app.geodataMessage, /Скачивание/);
  state = 'failed';
  await app.loadGeodata();
  assert.match(app.geodataMessage, /Контрольная сумма.*geosite.dat/);
});

test('geodata admission error is not hidden by a previous update failure', async () => {
  const app = appFixture();
  app.status = { controlEnabled: true };
  app.loadStatus = async () => {};
  const previous = { settings: {}, operation: { state: 'failed' }, failureLabel: 'Old checksum error' };
  app.geodata = previous;
  app.api = async url => {
    if (url === '/api/geodata/update') throw new Error('New update admission rejected');
    return previous;
  };
  await app.updateGeodata();
  assert.equal(app.geodataMessage, 'New update admission rejected');
});
