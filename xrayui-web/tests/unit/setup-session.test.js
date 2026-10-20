'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const express = require('express');
const http = require('node:http');
const fs = require('node:fs');
const vm = require('node:vm');
const registerSetup = require('../../routes/setup');

test('bootstrap authority is revoked in the same process after completion', () => {
  const session = registerSetup.createBootstrapSession('one-time');
  session.observe({ setupRequired: true, status: 'applying' });
  assert.equal(session.accepts('one-time'), true);
  session.observe({ setupRequired: false, status: 'configured' });
  assert.equal(session.accepts('one-time'), false);
  session.observe({ setupRequired: true, status: 'setup_required' });
  assert.equal(session.accepts('one-time'), false);
});

test('lost apply response cannot reopen bootstrap or repeat installation', async t => {
  let status = { setupRequired: true, status: 'setup_required' }, applies = 0;
  const app = express(); app.use(express.json());
  registerSetup(app, { bootstrapToken: 'one-time', helperClient: { async request(method, path) {
    if (path === '/v1/setup') return status;
    if (path === '/v1/setup/apply') {
      applies++; status = { setupRequired: false, status: 'configured', operation: { id: 'fixture', status: 'configured_direct' } };
      throw new Error('response lost');
    }
    throw new Error('unexpected call');
  } } });
  app.use((_, req, res, next) => res.status(503).json({ code: 'helper_unavailable' }));
  const server = http.createServer(app); await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const url = `http://127.0.0.1:${server.address().port}`;
  const apply = () => fetch(`${url}/api/setup/apply`, { method: 'POST', headers: { 'X-Bootstrap-Token': 'one-time', 'Content-Type': 'application/json' }, body: '{}' });
  assert.equal((await apply()).status, 503);
  assert.equal((await apply()).status, 410);
  assert.equal((await apply()).status, 401);
  assert.equal(applies, 1);
  const publicStatus = await (await fetch(`${url}/api/setup`)).json();
  assert.equal(publicStatus.operation.id, 'fixture');
  assert.equal(JSON.stringify(publicStatus).includes('one-time'), false);
});

test('browser adapter preserves helper status and code', async () => {
  let component;
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'), 'utf8'), {
    document: { addEventListener(_, callback) { callback(); } },
    Alpine: { data(_, factory) { component = factory(); } },
    window: { setTimeout, clearTimeout }, AbortController,
    fetch: async () => ({ ok: false, status: 409, headers: { get: () => 'application/json' }, json: async () => ({ code: 'setup_busy', message: 'Busy' }) })
  });
  await assert.rejects(component.api('/api/setup/plan'), error => error.code === 'setup_busy' && error.status === 409);
});

test('existing config adoption forwards only fingerprint and revokes bootstrap', async t => {
  let adoptions = 0;
  const app = express(); app.use(express.json());
  registerSetup(app, { bootstrapToken: 'one-time', helperClient: { async request(method, path, options) {
    if (path === '/v1/setup') return { setupRequired: true, configState: 'existing' };
    if (path === '/v1/setup/adopt') {
      adoptions++;
      assert.deepEqual(options.body, { configFingerprint: 'expected' });
      return { status: 'configured_direct' };
    }
    assert.fail(path);
  } } });
  const server = app.listen(0, '127.0.0.1'); await new Promise(resolve => server.once('listening', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const url = `http://127.0.0.1:${server.address().port}/api/setup/adopt`;
  const send = () => fetch(url, { method: 'POST', headers: { 'X-Bootstrap-Token': 'one-time', 'Content-Type': 'application/json' }, body: JSON.stringify({ configFingerprint: 'expected', ignored: 'not forwarded' }) });
  assert.equal((await send()).status, 200);
  assert.equal((await send()).status, 401);
  assert.equal(adoptions, 1);
});

test('setup replacement is forwarded only for an explicit boolean choice', async t => {
  const seen = [];
  const app = express(); app.use(express.json());
  registerSetup(app, { bootstrapToken: 'one-time', helperClient: { async request(method, path, options) {
    if (path === '/v1/setup') return { setupRequired: true, configState: 'existing' };
    assert.equal(path, '/v1/setup/plan'); seen.push(options.body); return { plan: {} };
  } } });
  const server = app.listen(0, '127.0.0.1'); await new Promise(resolve => server.once('listening', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const url = `http://127.0.0.1:${server.address().port}/api/setup/plan`;
  for (const replaceXray of [undefined, 'true', true]) {
    const response = await fetch(url, { method: 'POST', headers: { 'X-Bootstrap-Token': 'one-time', 'Content-Type': 'application/json' }, body: JSON.stringify({ interface: 'lan0', replaceXray, unknown: 'discard' }) });
    assert.equal(response.status, 200);
  }
  assert.deepEqual(seen, [{ interface: 'lan0', address: '' }, { interface: 'lan0', address: '' }, { interface: 'lan0', address: '', replaceXray: true }]);
});
