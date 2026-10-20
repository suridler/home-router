'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const express = require('express');
const { startFakeHelper, writeJSON } = require('../helpers/fake-helper');
const { createHelperClient } = require('../../services/helper-client');
const { createHelperAdminService } = require('../../services/helper-admin');
const registerRoutes = require('../../routes/proxy-domains');

test('web HTTP and Unix helper transport preserve canonical values and uncertain outcomes', async t => {
  let desired = ['old.example', '192.0.2.1'];
  let failNextRead = false;
  const calls = [];
  const helper = await startFakeHelper(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    calls.push([req.method, req.url, body]);
    if (req.method === 'GET') {
      if (failNextRead) {
        failNextRead = false;
        return writeJSON(res, 503, { error: { code: 'proxy_domain_unavailable', message: 'private diagnostic' } });
      }
      return writeJSON(res, 200, { desired, baseline: ['old.example'], consolidationActive: false });
    }
    if (req.method === 'POST') {
      const value = JSON.parse(body).domain;
      if (value === '2001:db8::1') {
        return writeJSON(res, 400, { error: { code: 'invalid_domain', message: 'private validation detail' } });
      }
      // This fake models the helper's canonical response, not a web validator.
      assert.equal(value, ' EXAMPLE.COM. ');
      if (!desired.includes('example.com')) desired = [...desired, 'example.com'];
      return writeJSON(res, 200, { domain: 'example.com', applied: true });
    }
    assert.equal(req.url, '/v1/proxy-domains/192.0.2.1');
    desired = desired.filter(value => value !== '192.0.2.1');
    return writeJSON(res, 503, { error: { code: 'domain_runtime_sync_failed', message: 'private runtime detail' } });
  });
  t.after(() => helper.close());
  const service = createHelperAdminService({ helperClient: createHelperClient({ socketPath: helper.socketPath }) });
  const app = express();
  app.use(express.json());
  registerRoutes(app, { ...service, controlEnabled: true });
  const server = await new Promise(resolve => {
    const instance = app.listen(0, '127.0.0.1', () => resolve(instance));
  });
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const base = `http://127.0.0.1:${server.address().port}/api/proxy-domains`;
  const post = domain => fetch(base, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ domain }) });

  let response = await post(' EXAMPLE.COM. ');
  let payload = await response.json();
  assert.equal(response.status, 200);
  assert.equal(payload.domain, 'example.com');
  assert.equal(payload.applied, true);
  assert.deepEqual(payload.snapshot.effectiveDomains, desired);

  response = await post('2001:db8::1');
  payload = await response.json();
  assert.equal(response.status, 400);
  assert.equal(payload.code, 'invalid_domain');
  assert.equal(payload.mutationMayHavePersisted, false);
  assert.match(payload.message, /IPv4/);
  assert.doesNotMatch(payload.message, /private/);

  failNextRead = true;
  response = await post(' EXAMPLE.COM. ');
  payload = await response.json();
  assert.equal(response.status, 503);
  assert.equal(payload.mutationMayHavePersisted, true);
  assert.doesNotMatch(payload.message, /private/);

  response = await fetch(`${base}/192.0.2.1`, { method: 'DELETE' });
  payload = await response.json();
  assert.equal(response.status, 503);
  assert.equal(payload.code, 'domain_runtime_sync_failed');
  assert.equal(payload.mutationMayHavePersisted, true);
  assert.doesNotMatch(payload.message, /private/);
  payload = await (await fetch(base)).json();
  assert.ok(payload.effectiveDomains.includes('example.com'));
  assert.ok(!payload.effectiveDomains.includes('192.0.2.1'));
  assert.equal(calls.filter(([method]) => method === 'POST').length, 3);
  assert.equal(calls.filter(([method]) => method === 'DELETE').length, 1);
});
