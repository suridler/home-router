'use strict';
const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const vm = require('node:vm');
const { createHelperUpdateService } = require('../../services/helper-updates');
const OP = '0123456789abcdef0123456789abcdef';
const ACTION = 'fedcba9876543210fedcba9876543210';
function fixture() {
  let create;
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'), 'utf8'), {
    document: { addEventListener: (_event, fn) => fn() },
    Alpine: { data: (_name, factory) => { create = factory; } },
    window: { confirm: () => true },
    crypto: require('node:crypto').webcrypto
  });
  const app = create();
  app.coreUpdate = { controlEnabled: true, currentVersion: '26.3.27', operation: null };
  return app;
}
test('core update service uses only fixed Xray endpoints with strict bodies', async () => {
  const calls = [];
  const service = createHelperUpdateService({ helperClient: { request: async (...args) => { calls.push(args); return {}; } } });
  await service.getCoreUpdateStatus();
  await service.checkCoreUpdate();
  await service.applyCoreUpdate(ACTION);
  await service.retryCoreUpdate(OP, ACTION);
  await service.rollbackCoreUpdate(OP, ACTION);
  assert.deepEqual(calls.map(call => call[1]), ['/v1/xray-upgrade', '/v1/xray-upgrade/check', '/v1/xray-upgrade/apply', '/v1/xray-upgrade/retry', '/v1/xray-upgrade/rollback']);
  assert.deepEqual(calls[2][2].body, { actionId: ACTION });
  assert.deepEqual(calls[4][2].body, { operationId: OP, actionId: ACTION });
});
test('failed Xray restart never triggers automatic recovery in the panel', async () => {
  const app = fixture();
  const paths = [];
  app.api = async path => { paths.push(path); return { controlEnabled: true, operation: { id: OP, state: 'failed', stage: 'restart_xray', error: 'xray_restart_failed', backupReady: true } }; };
  await app.loadCoreUpdate();
  assert.equal(app.coreUpdateFailed(), true);
  assert.equal(app.coreUpdate.operation.backupReady, true);
  assert.match(app.coreUpdateStage(), /Ошибка/);
  assert.deepEqual(paths, ['/api/xray-upgrade']);
});
test('explicit recovery sends selected action and returns to status polling', async () => {
  const app = fixture();
  app.coreUpdate.operation = { id: OP, state: 'failed', backupReady: true };
  const calls = [];
  app.api = async (path, options) => {
    calls.push({ path, options });
    return { controlEnabled: true, operation: { id: OP, state: 'running', actionId: ACTION } };
  };
  app.newUpdateActionId = () => ACTION;
  await app.runCoreUpdate('rollback');
  assert.equal(calls[0].path, '/api/xray-upgrade/rollback');
  assert.equal(calls[0].options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(calls[0].options.body), { operationId: OP, actionId: ACTION });
  assert.equal(calls[1].path, '/api/xray-upgrade');
});
test('lost response retains the same action ID, while an explicit rejection allows another choice', async () => {
  const app = fixture();
  app.coreUpdate.operation = { id: OP, state: 'failed', backupReady: true };
  app.newUpdateActionId = () => ACTION;
  app.api = async path => {
    if (path.endsWith('/retry')) throw new Error('response lost');
    return { controlEnabled: true, operation: { id: OP, state: 'failed', backupReady: true } };
  };
  await app.runCoreUpdate('retry');
  assert.equal(app.coreAction.actionId, ACTION);
  app.api = async path => {
    if (path.endsWith('/retry')) throw Object.assign(new Error('rejected'), { status: 409 });
    return { controlEnabled: true, operation: { id: OP, state: 'failed', backupReady: true } };
  };
  await app.runCoreUpdate('retry');
  assert.equal(app.coreAction, null);
});

test('web core-update routes enforce control policy and reject path parameters', async t => {
  const express = require('express');
  const register = require('../../routes/updates');
  const backend = {
    controlEnabled: false,
    getCoreUpdateStatus: async () => ({ currentVersion: '26.3.27', operation: null }),
    applyCoreUpdate: async actionId => ({ operation: { id: OP, actionId, state: 'launching' } })
  };
  const app = express(); app.use(express.json()); register(app, backend);
  const server = app.listen(0, '127.0.0.1');
  await new Promise(resolve => server.once('listening', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const base = `http://127.0.0.1:${server.address().port}/api/xray-upgrade`;
  const post = body => fetch(`${base}/apply`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  assert.equal((await post({ actionId: ACTION })).status, 403);
  assert.equal((await (await fetch(base)).json()).controlEnabled, false);
  backend.controlEnabled = true;
  assert.equal((await post({ actionId: ACTION, binary: '/tmp/untrusted' })).status, 400);
  const response = await post({ actionId: ACTION });
  assert.equal(response.status, 202);
  assert.equal((await response.json()).operation.state, 'launching');
});
