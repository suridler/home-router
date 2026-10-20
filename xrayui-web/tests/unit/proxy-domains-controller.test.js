'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createProxyDomainsController } = require('../../controllers/proxy-domains');

function response() {
  return {
    statusCode: 200,
    payload: null,
    status(code) { this.statusCode = code; return this; },
    json(payload) { this.payload = payload; return this; }
  };
}

function controller(overrides = {}) {
  return createProxyDomainsController({
    controlEnabled: true,
    getProxyDomains: async () => ({}),
    addProxyDomain: async () => ({}),
    deleteProxyDomain: async () => ({}),
    ...overrides
  });
}

function helperError(code, statusCode, mutationMayHavePersisted, message = 'raw helper detail') {
  return Object.assign(new Error(message), { code, statusCode, mutationMayHavePersisted });
}

test('list returns the helper projection unchanged under ok', async () => {
  const snapshot = {
    permanentDomains: ['old.example'],
    pendingAdditions: ['192.0.2.1'],
    pendingDeletions: [],
    effectiveDomains: ['old.example', '192.0.2.1'],
    runtime: { state: 'active' },
    consolidationActive: false
  };
  const res = response();
  await controller({ getProxyDomains: async () => snapshot }).list({}, res);

  assert.equal(res.statusCode, 200);
  assert.deepEqual(res.payload, { ok: true, ...snapshot });
});

test('add and remove pass raw inputs through and return canonical mutation results', async () => {
  let added;
  let removed;
  const result = {
    domain: 'example.com',
    applied: true,
    snapshot: { permanentDomains: ['example.com'], pendingAdditions: [], pendingDeletions: [], effectiveDomains: ['example.com'], runtime: { state: 'active' }, consolidationActive: false }
  };
  const service = controller({
    addProxyDomain: async domain => { added = domain; return result; },
    deleteProxyDomain: async domain => { removed = domain; return result; }
  });

  const addResponse = response();
  await service.add({ body: { domain: '  Example.COM  ' } }, addResponse);
  assert.equal(added, '  Example.COM  ');
  assert.equal(addResponse.statusCode, 200);
  assert.deepEqual(addResponse.payload, { ok: true, ...result });

  const removeResponse = response();
  await service.remove({ params: { domain: 'OLD.EXAMPLE' } }, removeResponse);
  assert.equal(removed, 'OLD.EXAMPLE');
  assert.equal(removeResponse.statusCode, 200);
  assert.deepEqual(removeResponse.payload, { ok: true, ...result });
});

test('disabled control keeps the existing 503 JSON and does not call the helper service', async () => {
  let calls = 0;
  const service = controller({
    controlEnabled: false,
    addProxyDomain: async () => { calls += 1; },
    deleteProxyDomain: async () => { calls += 1; }
  });

  for (const action of [
    { handler: 'add', request: { body: { domain: 'example.com' } } },
    { handler: 'remove', request: { params: { domain: 'example.com' } } }
  ]) {
    const res = response();
    await service[action.handler](action.request, res);
    assert.equal(res.statusCode, 503);
    assert.deepEqual(res.payload, {
      ok: false,
      message: 'control API is disabled; enable [web].control_enabled in helper and restart helper'
    });
  }
  assert.equal(calls, 0);
});

test('known helper codes keep status, code and flag while exposing only a safe message', async () => {
  const cases = [
    ['invalid_domain', 400, false],
    ['consolidation_active', 409, false],
    ['domain_runtime_sync_failed', 503, true],
    ['domain_runtime_apply_failed', 503, true],
    ['proxy_domain_unavailable', 503, true],
    ['helper_timeout', 503, true],
    ['helper_unavailable', 503, true],
    ['helper_invalid_response', 502, true],
    ['helper_response_too_large', 502, true],
    ['invalid_request', 400, false]
  ];

  for (const [code, statusCode, flag] of cases) {
    const secret = `/run/secret/${code}.sock raw helper text`;
    const failure = helperError(code, statusCode, flag, secret);
    const res = response();
    await controller({ addProxyDomain: async () => { throw failure; } }).add({ body: { domain: 'example.com' } }, res);

    assert.equal(res.statusCode, statusCode, code);
    assert.equal(res.payload.ok, false, code);
    assert.equal(res.payload.code, code, code);
    assert.equal(res.payload.mutationMayHavePersisted, flag, code);
    assert.equal(typeof res.payload.message, 'string', code);
    assert.ok(res.payload.message.length > 0, code);
    assert.doesNotMatch(res.payload.message, /secret|raw helper text|\/run\//, code);
  }
});

test('unknown errors keep the fallback code and never expose the raw message', async () => {
  const res = response();
  const failure = helperError('internal_failure', 500, true, 'ENOENT /etc/xray/config.json secret content');
  await controller({ deleteProxyDomain: async () => { throw failure; } }).remove({ params: { domain: 'example.com' } }, res);

  assert.equal(res.statusCode, 500);
  assert.equal(res.payload.ok, false);
  assert.equal(res.payload.code, 'internal_failure');
  assert.equal(res.payload.mutationMayHavePersisted, true);
  assert.equal(res.payload.message, 'Не удалось выполнить операцию со списком доменов и IPv4.');
  assert.doesNotMatch(res.payload.message, /ENOENT|config\.json|secret/);
});

test('errors without code or status use the existing fallback status and code', async () => {
  const res = response();
  await controller({ getProxyDomains: async () => { throw new Error('raw failure'); } }).list({}, res);

  assert.equal(res.statusCode, 500);
  assert.equal(res.payload.code, 'proxy_domain_failed');
  assert.equal(res.payload.mutationMayHavePersisted, false);
  assert.equal(res.payload.message, 'Не удалось выполнить операцию со списком доменов и IPv4.');
});

test('error responses are not written to the console', async () => {
  const original = { error: console.error, warn: console.warn, log: console.log };
  const logged = [];
  console.error = (...args) => logged.push(args);
  console.warn = (...args) => logged.push(args);
  console.log = (...args) => logged.push(args);
  try {
    const res = response();
    await controller({
      addProxyDomain: async () => { throw helperError('domain_runtime_sync_failed', 503, true); }
    }).add({ body: { domain: 'example.com' } }, res);
    assert.equal(res.statusCode, 503);
    assert.deepEqual(logged, []);
  } finally {
    console.error = original.error;
    console.warn = original.warn;
    console.log = original.log;
  }
});
