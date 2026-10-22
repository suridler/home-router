'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const { test } = require('node:test');
// The default backend is not used; requests use the explicit fake helper below.
process.env.XRAY_CORE_HELPER_SOCKET = '/unused-test-helper.sock';
const { createApp } = require('../../app');
const { createBackend } = require('../../services/backend');
const { createHelperClient } = require('../../services/helper-client');
const { startFakeHelper, writeJSON } = require('../helpers/fake-helper');

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

test('routing and outbound mutations preserve helper contracts and errors', async t => {
  const calls = [];
  const fake = await startFakeHelper(async (request, response) => {
    const body = request.method === 'GET' ? null : await readJSON(request);
    calls.push({ method: request.method, path: request.url, body });
    if (request.url === '/v1/xray/outbound-drafts/import') {
      return writeJSON(response, 200, { outbound: { tag: 'draft', protocol: 'vless', settings: { id: 'test-secret' } }, warnings: ['Review parser output'] });
    }
    if (request.url === '/v1/xray/routing/rules' && request.method === 'POST') {
      return writeJSON(response, 200, { generation: 8, contentHash: 'next' });
    }
    if (request.url === '/v1/xray/outbound-drafts/apply' && request.method === 'POST') {
      return writeJSON(response, 200, { generation: 9, contentHash: 'outbound-next' });
    }
    if (request.url === '/v1/xray/routing/rules/clone' && request.method === 'POST') {
      return writeJSON(response, 200, { generation: 10, contentHash: 'clone-next' });
    }
    if (request.url === '/v1/xray/outbounds/stale' && request.method === 'DELETE') {
      return writeJSON(response, 409, { error: { code: 'generation_conflict', message: 'configuration generation changed' } });
    }
    if (request.url === '/v1/xray/routing/rules/gateway' && request.method === 'DELETE') {
      return writeJSON(response, 403, { error: { code: 'rule_protected', message: 'TProxy gateway rule cannot be removed' } });
    }
    if (request.url === '/v1/xray/routing/rules/user-a' && request.method === 'PUT') {
      return writeJSON(response, 503, { error: { code: 'rollback_failed', message: 'configuration rollback could not be completed' } });
    }
    return writeJSON(response, 404, { error: { code: 'not_found', message: 'not found' } });
  });
  t.after(() => fake.close());
  const services = createBackend({
    client: createHelperClient({ socketPath: fake.socketPath }),
    controlEnabled: true,
    trafficReader: async () => ({})
  });
  const web = await startWeb(createApp(services));
  t.after(() => web.close());

  const preview = await fetch(`${web.baseURL}/api/outbound-drafts/import`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uri: 'vless://secret', tag: 'proxy' })
  });
  assert.equal(preview.status, 200);
  assert.equal((await preview.json()).outbound.protocol, 'vless');
  assert.equal(preview.headers.get('cache-control'), 'no-store');

  const add = await fetch(`${web.baseURL}/api/routing/rules`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      expectedGeneration: 7,
      position: 3,
      restart: false,
      rule: { ruleTag: 'user rule', conditions: { domain: 'domain:example.com\ngeosite:test' }, outboundTag: 'proxy' }
    })
  });
  assert.equal(add.status, 200);
  assert.equal((await add.json()).generation, 8);
  const helperAdd = calls.find(call => call.path === '/v1/xray/routing/rules');
  assert.deepEqual(helperAdd.body.rule.conditions.domain, ['domain:example.com', 'geosite:test']);
  assert.equal(helperAdd.body.expectedGeneration, 7);

  const outbound = await fetch(`${web.baseURL}/api/outbound-drafts/apply`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      expectedGeneration: 8, outbound: { tag: 'backup', protocol: 'trojan', settings: { password: 'test-secret' } }, restart: true
    })
  });
  assert.equal(outbound.status, 200);
  assert.equal((await outbound.json()).generation, 9);
  const helperOutbound = calls.find(call => call.path === '/v1/xray/outbound-drafts/apply' && call.method === 'POST');
  assert.deepEqual(helperOutbound.body, { outbound: { tag: 'backup', protocol: 'trojan', settings: { password: 'test-secret' } }, restart: true, expectedGeneration: 8, confirmReplacement: false });

  const clone = await fetch(`${web.baseURL}/api/routing/rules/clone`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      expectedGeneration: 9, position: 4, ruleTag: 'user-copy', restart: false
    })
  });
  assert.equal(clone.status, 200);
  assert.equal((await clone.json()).generation, 10);

  const stale = await fetch(`${web.baseURL}/api/outbounds/stale`, {
    method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expectedGeneration: 6, restart: false })
  });
  assert.equal(stale.status, 409);
  assert.equal((await stale.json()).code, 'generation_conflict');

  const protectedRule = await fetch(`${web.baseURL}/api/routing/rules/gateway`, {
    method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expectedGeneration: 7, restart: false })
  });
  assert.equal(protectedRule.status, 403);
  assert.equal((await protectedRule.json()).code, 'rule_protected');

  const rollback = await fetch(`${web.baseURL}/api/routing/rules/user-a`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      expectedGeneration: 7,
      restart: true,
      rule: { ruleTag: 'user-a', conditions: { domain: ['domain:example.com'] }, outboundTag: 'proxy' }
    })
  });
  assert.equal(rollback.status, 503);
  assert.equal((await rollback.json()).code, 'rollback_failed');
  assert.equal(calls.some(call => call.path.includes('/unrelated')), false);
});

test('browser mutations send the displayed generation and refresh stale state', () => {
  const source = fs.readFileSync(require.resolve('../../public/app.js'), 'utf8');
  assert.match(source, /expectedGeneration: this\.status\?\.generation/);
  assert.match(source, /error\.code === 'generation_conflict'[^;]*await this\.loadStatus\(\)/);
  assert.match(source, /encodeURIComponent\(rule\.tag\)/);
  assert.doesNotMatch(source, /routing\/rules\/\$\{rule\.index\}/);
});
