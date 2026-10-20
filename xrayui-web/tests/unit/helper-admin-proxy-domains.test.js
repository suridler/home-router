'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createHelperAdminService } = require('../../services/helper-admin');
const { HelperClientError, LONG_OPERATION_TIMEOUT_MS } = require('../../services/helper-client');

function snapshot(overrides = {}) {
  return {
    desired: ['new.example', '192.0.2.1'],
    baseline: ['old.example', '192.0.2.1'],
    consolidationActive: false,
    ...overrides
  };
}

function fakeClient(handler) {
  const calls = [];
  return {
    calls,
    async request(method, path, options = {}) {
      calls.push({ method, path, options });
      return handler(method, path, options, calls);
    }
  };
}

test('proxy-domain snapshot is projected from strict helper arrays', async () => {
  const client = fakeClient(() => ({
    desired: ['new.example', '192.0.2.1'],
    baseline: ['old.example', '192.0.2.1'],
    consolidationActive: true
  }));
  const service = createHelperAdminService({ helperClient: client });

  const result = await service.getProxyDomains();

  assert.deepEqual(result, {
    permanentDomains: ['old.example', '192.0.2.1'],
    pendingAdditions: ['new.example'],
    pendingDeletions: ['old.example'],
    effectiveDomains: ['new.example', '192.0.2.1'],
    runtime: { state: 'consolidating' },
    consolidationActive: true
  });
  assert.deepEqual(client.calls.map(call => [call.method, call.path]), [['GET', '/v1/proxy-domains']]);
});

test('proxy-domain snapshot rejects malformed or incomplete helper responses', async () => {
  const malformed = [
    ['missing response', undefined],
    ['null response', null],
    ['array response', []],
    ['missing both arrays', { consolidationActive: false }],
    ['missing desired', { baseline: [], consolidationActive: false }],
    ['missing baseline', { desired: [], consolidationActive: false }],
    ['missing consolidationActive', { desired: [], baseline: [] }],
    ['string consolidationActive', { desired: [], baseline: [], consolidationActive: 'false' }],
    ['numeric consolidationActive', { desired: [], baseline: [], consolidationActive: 0 }],
    ['null consolidationActive', { desired: [], baseline: [], consolidationActive: null }],
    ['non-array desired', { desired: 'example.com', baseline: [], consolidationActive: false }],
    ['non-array baseline', { desired: [], baseline: {}, consolidationActive: false }],
    ['non-string desired', { desired: ['example.com', 42], baseline: [], consolidationActive: false }],
    ['null desired item', { desired: [null], baseline: [], consolidationActive: false }],
    ['non-string baseline', { desired: [], baseline: ['example.com', false], consolidationActive: false }]
  ];

  for (const [name, payload] of malformed) {
    const service = createHelperAdminService({ helperClient: fakeClient(() => payload) });
    await assert.rejects(service.getProxyDomains(), error => {
      assert.ok(error instanceof HelperClientError, name);
      assert.equal(error.code, 'helper_invalid_response', name);
      assert.equal(error.statusCode, 502, name);
      return true;
    });
  }
});

test('add returns canonical domain from helper and validates applied', async () => {
  const client = fakeClient((method, path) => {
    if (method === 'POST' && path === '/v1/proxy-domains') return { domain: 'example.com', applied: true };
    if (method === 'GET' && path === '/v1/proxy-domains') return snapshot({ desired: ['example.com'], baseline: [] });
    throw new Error(`unexpected ${method} ${path}`);
  });
  const service = createHelperAdminService({ helperClient: client });

  const result = await service.addProxyDomain('  Example.COM  ');

  assert.equal(result.domain, 'example.com');
  assert.equal(result.applied, true);
  assert.deepEqual(result.snapshot, {
    permanentDomains: [],
    pendingAdditions: ['example.com'],
    pendingDeletions: [],
    effectiveDomains: ['example.com'],
    runtime: { state: 'active' },
    consolidationActive: false
  });
  assert.deepEqual(client.calls.map(call => [call.method, call.path]), [
    ['POST', '/v1/proxy-domains'],
    ['GET', '/v1/proxy-domains']
  ]);
  assert.deepEqual(client.calls[0].options.body, { domain: '  Example.COM  ' });
  assert.equal(client.calls[0].options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
});

test('applied=false is preserved and not turned into success', async () => {
  const client = fakeClient((method) => {
    if (method === 'POST') return { domain: 'pending.example', applied: false };
    return snapshot({ desired: ['pending.example'], baseline: [] });
  });
  const service = createHelperAdminService({ helperClient: client });

  const result = await service.addProxyDomain('pending.example');

  assert.equal(result.domain, 'pending.example');
  assert.equal(result.applied, false);
  assert.deepEqual(result.snapshot.pendingAdditions, ['pending.example']);
});

test('delete uses the encoded path without a body and returns the canonical domain', async () => {
  const client = fakeClient((method, path) => {
    if (method === 'DELETE') return { domain: 'old example/one', applied: true };
    if (method === 'GET') return snapshot({ desired: [], baseline: [] });
    throw new Error(`unexpected ${method} ${path}`);
  });
  const service = createHelperAdminService({ helperClient: client });

  const result = await service.deleteProxyDomain('OLD EXAMPLE/ONE');

  assert.equal(result.domain, 'old example/one');
  assert.equal(result.applied, true);
  assert.deepEqual(client.calls.map(call => [call.method, call.path]), [
    ['DELETE', '/v1/proxy-domains/OLD%20EXAMPLE%2FONE'],
    ['GET', '/v1/proxy-domains']
  ]);
  assert.equal(client.calls[0].options.body, undefined);
  assert.equal(client.calls[0].options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
});

test('malformed mutation responses are rejected as invalid without reading the snapshot', async () => {
  const malformed = [
    ['missing response', undefined],
    ['null response', null],
    ['array response', []],
    ['missing domain', { applied: true }],
    ['missing applied', { domain: 'example.com' }],
    ['non-string domain', { domain: 42, applied: true }],
    ['string applied', { domain: 'example.com', applied: 'true' }],
    ['numeric applied', { domain: 'example.com', applied: 1 }]
  ];

  for (const [name, payload] of malformed) {
    const client = fakeClient((method) => method === 'POST' ? payload : snapshot());
    const service = createHelperAdminService({ helperClient: client });

    await assert.rejects(service.addProxyDomain('example.com'), error => {
      assert.ok(error instanceof HelperClientError, name);
      assert.equal(error.code, 'helper_invalid_response', name);
      assert.equal(error.statusCode, 502, name);
      assert.equal(error.mutationMayHavePersisted, true, name);
      return true;
    });
    assert.deepEqual(client.calls.map(call => call.method), ['POST'], name);
  }
});

test('runtime sync and apply failures mark the mutation as possibly persisted without a snapshot read', async () => {
  for (const code of ['domain_runtime_sync_failed', 'domain_runtime_apply_failed']) {
    const failure = new HelperClientError('raw helper detail', { code, statusCode: 503 });
    const client = fakeClient(() => { throw failure; });
    const service = createHelperAdminService({ helperClient: client });

    await assert.rejects(service.addProxyDomain('example.com'), error => {
      assert.equal(error, failure);
      assert.equal(error.statusCode, 503);
      assert.equal(error.mutationMayHavePersisted, true);
      return true;
    });
    assert.deepEqual(client.calls.map(call => call.method), ['POST']);
  }
});

test('transport failures on a mutation are marked as possibly persisted', async () => {
  const failures = [
    new HelperClientError('timeout', { code: 'helper_timeout', statusCode: 503 }),
    new HelperClientError('unavailable', { code: 'helper_unavailable', statusCode: 503 }),
    new HelperClientError('invalid json', { code: 'helper_invalid_response' }),
    new HelperClientError('too large', { code: 'helper_response_too_large' })
  ];

  for (const failure of failures) {
    const client = fakeClient(() => { throw failure; });
    const service = createHelperAdminService({ helperClient: client });

    await assert.rejects(service.deleteProxyDomain('example.com'), error => {
      assert.equal(error, failure);
      assert.equal(error.mutationMayHavePersisted, true);
      return true;
    });
    assert.deepEqual(client.calls.map(call => call.method), ['DELETE']);
  }
});

test('definitive pre-write rejections are not marked as possibly persisted', async () => {
  const failures = [
    new HelperClientError('invalid domain', { code: 'invalid_domain', statusCode: 400 }),
    new HelperClientError('consolidation active', { code: 'consolidation_active', statusCode: 409 })
  ];

  for (const failure of failures) {
    const client = fakeClient(() => { throw failure; });
    const service = createHelperAdminService({ helperClient: client });

    await assert.rejects(service.addProxyDomain('example.com'), error => {
      assert.equal(error, failure);
      assert.equal(error.mutationMayHavePersisted, false);
      return true;
    });
  }
});

test('an existing mutationMayHavePersisted=true is never cleared by the service', async () => {
  const failure = Object.assign(new Error('opaque failure'), { code: 'unknown_failure', statusCode: 400, mutationMayHavePersisted: true });
  const client = fakeClient(() => { throw failure; });
  const service = createHelperAdminService({ helperClient: client });

  await assert.rejects(service.addProxyDomain('example.com'), error => {
    assert.equal(error, failure);
    assert.equal(error.mutationMayHavePersisted, true);
    return true;
  });
});

test('a successful mutation followed by a snapshot read failure stays uncertain and is not repeated', async () => {
  const readFailure = new HelperClientError('snapshot unavailable', { code: 'helper_unavailable', statusCode: 503 });
  const client = fakeClient(method => {
    if (method === 'POST') return { domain: 'example.com', applied: true };
    throw readFailure;
  });
  const service = createHelperAdminService({ helperClient: client });

  await assert.rejects(service.addProxyDomain('example.com'), error => {
    assert.equal(error, readFailure);
    assert.equal(error.mutationMayHavePersisted, true);
    return true;
  });
  assert.deepEqual(client.calls.map(call => call.method), ['POST', 'GET']);
});

test('a successful mutation followed by a malformed snapshot is invalid and uncertain', async () => {
  const client = fakeClient(method => {
    if (method === 'POST') return { domain: 'example.com', applied: true };
    return { desired: ['example.com'], baseline: [], consolidationActive: 'no' };
  });
  const service = createHelperAdminService({ helperClient: client });

  await assert.rejects(service.addProxyDomain('example.com'), error => {
    assert.equal(error.code, 'helper_invalid_response');
    assert.equal(error.statusCode, 502);
    assert.equal(error.mutationMayHavePersisted, true);
    return true;
  });
  assert.deepEqual(client.calls.map(call => call.method), ['POST', 'GET']);
});
