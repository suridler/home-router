'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createApp } = require('../../app');
const { createBackend } = require('../../services/backend');
const { createHelperClient, LONG_OPERATION_TIMEOUT_MS, MAX_REQUEST_TIMEOUT_MS } = require('../../services/helper-client');
const { createHelperUpdateService } = require('../../services/helper-updates');
const { startFakeHelper, writeJSON } = require('../helpers/fake-helper');

const OP = '0123456789abcdef0123456789abcdef';
const ACTION = 'fedcba9876543210fedcba9876543210';
const OPERATION = {
  id: OP,
  sourceVersion: 'helper-v0.1.0',
  targetVersion: 'helper-v0.2.0',
  state: 'prepared',
  stage: 'prepared',
  snapshotReady: false,
  attempts: []
};

async function readJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

async function startWeb(app) {
  const server = app.listen(0, '127.0.0.1');
  await new Promise((resolve, reject) => {
    server.once('listening', resolve);
    server.once('error', reject);
  });
  return {
    baseURL: `http://127.0.0.1:${server.address().port}`,
    close: () => new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  };
}

test('typed update client uses only fixed helper lifecycle endpoints', async () => {
  const calls = [];
  const client = {
    async request(method, path, options = {}) {
      calls.push({ method, path, options });
      if (method === 'GET') return { currentVersion: 'helper-v0.1.0', currentCommit: 'abc', operation: null };
      if (path.endsWith('/check')) return { currentVersion: 'helper-v0.1.0', latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' };
      return { operation: { ...OPERATION, targetVersion: 'helper-v0.2.0' } };
    }
  };
  const service = createHelperUpdateService({ helperClient: client });

  assert.equal((await service.getUpdateStatus()).currentVersion, 'helper-v0.1.0');
  await service.checkUpdate();
  await service.prepareUpdate('helper-v0.2.0');
  await service.applyUpdate(OP, ACTION);
  await service.retryUpdate(OP, ACTION);
  await service.rollbackUpdate(OP, ACTION);

  assert.deepEqual(calls.map(call => [call.method, call.path]), [
    ['GET', '/v1/update'],
    ['POST', '/v1/update/check'],
    ['POST', '/v1/update/prepare'],
    ['POST', '/v1/update/apply'],
    ['POST', '/v1/update/retry'],
    ['POST', '/v1/update/rollback']
  ]);
  assert.deepEqual(calls[1].options.body, {});
  assert.deepEqual(calls[2].options.body, { version: 'helper-v0.2.0' });
  for (const call of calls.slice(3)) assert.deepEqual(call.options.body, { operationId: OP, actionId: ACTION });
  assert.equal(calls[1].options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  assert.equal(calls[2].options.timeoutMs, MAX_REQUEST_TIMEOUT_MS);
  for (const call of calls.slice(3)) assert.equal(call.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  assert.throws(() => createHelperUpdateService({}), /helperClient is required/);
});

test('web update routes forward the matching status and lifecycle payloads', async t => {
  const calls = [];
  const fake = await startFakeHelper(async (request, response) => {
    const body = request.method === 'GET' ? null : await readJSON(request);
    calls.push({ method: request.method, path: request.url, body });
    if (request.url === '/v1/update' && request.method === 'GET') {
      return writeJSON(response, 200, { currentVersion: 'helper-v0.1.0', currentCommit: 'abc123', operation: null, controlEnabled: false });
    }
    if (request.url === '/v1/update/check') {
      return writeJSON(response, 200, { currentVersion: 'helper-v0.1.0', currentCommit: 'abc123', latestVersion: 'helper-v0.2.0', updateAvailable: true, comparison: 'known' });
    }
    if (request.url === '/v1/update/prepare') return writeJSON(response, 200, { operation: OPERATION });
    if (request.url.startsWith('/v1/update/')) return writeJSON(response, 200, { operation: { ...OPERATION, state: 'launching' } });
    return writeJSON(response, 404, { error: { code: 'not_found', message: 'not found' } });
  });
  t.after(() => fake.close());

  const backend = createBackend({
    client: createHelperClient({ socketPath: fake.socketPath }),
    controlEnabled: true,
    trafficReader: async () => ({})
  });
  const web = await startWeb(createApp(backend));
  t.after(() => web.close());

  const status = await fetch(`${web.baseURL}/api/helper-update`);
  assert.equal(status.status, 200);
  const statusBody = await status.json();
  assert.equal(statusBody.currentVersion, 'helper-v0.1.0');
  assert.equal(statusBody.controlEnabled, true, 'web policy must override helper data');

  const check = await fetch(`${web.baseURL}/api/helper-update/check`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
  assert.equal(check.status, 200);
  assert.equal((await check.json()).latestVersion, 'helper-v0.2.0');
  assert.deepEqual(calls.find(call => call.path === '/v1/update/check').body, {});

  const prepare = await fetch(`${web.baseURL}/api/helper-update/prepare`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version: 'helper-v0.2.0' })
  });
  assert.equal(prepare.status, 200);
  assert.equal((await prepare.json()).operation.targetVersion, 'helper-v0.2.0');
  assert.deepEqual(calls.find(call => call.path === '/v1/update/prepare').body, { version: 'helper-v0.2.0' });

  for (const action of ['apply', 'retry', 'rollback']) {
    const response = await fetch(`${web.baseURL}/api/helper-update/${action}`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ operationId: OP, actionId: ACTION })
    });
    assert.equal(response.status, 200);
    assert.equal((await response.json()).operation.state, 'launching');
  }
  for (const action of ['apply', 'retry', 'rollback']) {
    assert.deepEqual(calls.find(call => call.path === `/v1/update/${action}`).body, { operationId: OP, actionId: ACTION });
  }
  assert.equal(calls.some(call => !['/v1/update', '/v1/update/check', '/v1/update/prepare', '/v1/update/apply', '/v1/update/retry', '/v1/update/rollback'].includes(call.path)), false);
});

test('all update POST routes are denied with 403 when control is disabled, including check', async t => {
  let helperCalls = 0;
  const fake = await startFakeHelper((request, response) => {
    helperCalls += 1;
    writeJSON(response, 500, { error: { code: 'unexpected', message: 'helper should not be called' } });
  });
  t.after(() => fake.close());
  const backend = createBackend({
    client: createHelperClient({ socketPath: fake.socketPath }),
    controlEnabled: false,
    trafficReader: async () => ({})
  });
  const web = await startWeb(createApp(backend));
  t.after(() => web.close());

  const responses = await Promise.all(['check', 'prepare', 'apply', 'retry', 'rollback'].map(action => fetch(`${web.baseURL}/api/helper-update/${action}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}'
  })));
  for (const response of responses) {
    assert.equal(response.status, 403);
    assert.equal((await response.json()).code, 'control_disabled');
  }
  assert.equal(helperCalls, 0);
});

test('update routes reject unknown fields, invalid IDs and non-exact versions before calling helper', async t => {
  let helperCalls = 0;
  const fake = await startFakeHelper((request, response) => {
    helperCalls += 1;
    writeJSON(response, 500, { error: { code: 'unexpected', message: 'helper should not be called' } });
  });
  t.after(() => fake.close());
  const backend = createBackend({
    client: createHelperClient({ socketPath: fake.socketPath }),
    controlEnabled: true,
    trafficReader: async () => ({})
  });
  const web = await startWeb(createApp(backend));
  t.after(() => web.close());

  const cases = [
    ['check', { extra: true }],
    ['prepare', { version: 'latest' }],
    ['prepare', { version: 'helper-v1.2' }],
    ['prepare', { version: 'helper-v1.2.3', extra: true }],
    ['prepare', {}],
    ['apply', { operationId: OP.toUpperCase(), actionId: ACTION }],
    ['retry', { operationId: OP, actionId: 'short' }],
    ['rollback', { operationId: OP, actionId: ACTION, extra: true }]
  ];
  for (const [action, body] of cases) {
    const response = await fetch(`${web.baseURL}/api/helper-update/${action}`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
    });
    assert.equal(response.status, 400, `${action} ${JSON.stringify(body)}`);
    assert.equal((await response.json()).code, 'invalid_request');
  }
  assert.equal(helperCalls, 0);
});

test('typed helper update failures keep helper status and code', async t => {
  const fake = await startFakeHelper((request, response) => {
    if (request.url === '/v1/update/apply') {
      return writeJSON(response, 409, { error: { code: 'update_busy', message: 'another installation is pending' } });
    }
    writeJSON(response, 404, { error: { code: 'not_found', message: 'not found' } });
  });
  t.after(() => fake.close());
  const backend = createBackend({
    client: createHelperClient({ socketPath: fake.socketPath }),
    controlEnabled: true,
    trafficReader: async () => ({})
  });
  const web = await startWeb(createApp(backend));
  t.after(() => web.close());

  const response = await fetch(`${web.baseURL}/api/helper-update/apply`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operationId: OP, actionId: ACTION })
  });
  assert.equal(response.status, 409);
  assert.deepEqual(await response.json(), { ok: false, code: 'update_busy', message: 'another installation is pending' });
});
