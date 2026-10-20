'use strict';

const assert = require('node:assert/strict');
const { execFile } = require('node:child_process');
const { after, before, test } = require('node:test');
const { promisify } = require('node:util');
const { app } = require('../../app');

const execFileAsync = promisify(execFile);

let server;
let baseUrl;

before(async () => {
  server = app.listen(0, '127.0.0.1');
  await new Promise((resolve, reject) => {
    server.once('listening', resolve);
    server.once('error', reject);
  });
  const address = server.address();
  baseUrl = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  if (!server) return;
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
});

test('host routing services are active', async () => {
  const { stdout } = await execFileAsync('systemctl', [
    'is-active', 'xray.service', 'xray-tproxy-routing.service'
  ]);
  assert.deepEqual(stdout.trim().split(/\r?\n/), ['active', 'active']);
});

test('status sees the active Xray service', async () => {
  const response = await fetch(`${baseUrl}/api/status`);
  assert.equal(response.status, 200);
  const status = await response.json();
  assert.equal(status.xray, 'running', 'xray.service must be active on the integration host');
});

test('status exposes the active host routing summary without secrets', async context => {
  const response = await fetch(`${baseUrl}/api/status`);
  assert.equal(response.status, 200);
  const status = await response.json();
  if (status.configError && /EACCES|permission denied/i.test(status.configError)) {
    context.skip('run with sudo to inspect the root-readable active Xray config');
    return;
  }
  if (status.configError) {
    assert.equal(status.configState, 'drift');
    assert.match(status.configError, /differs from committed state/i);
  } else {
    assert.equal(status.configState, 'parsed');
  }
  assert.ok(status.routing && typeof status.routing === 'object');
  assert.equal(typeof status.routing.domainStrategy, 'string');
  assert.ok(Array.isArray(status.routing.rules));
  assert.ok(Array.isArray(status.routing.balancers));

  const conditionKeys = new Set([
    'inboundTag', 'domain', 'ip', 'port', 'network', 'protocol',
    'source', 'sourcePort', 'user', 'email', 'domainMatcher'
  ]);
  status.routing.rules.forEach((rule, offset) => {
    assert.equal(rule.index, offset + 1, 'routing rule order must be preserved');
    assert.deepEqual(
      Object.keys(rule).sort(),
      ['balancerTag', 'conditions', 'description', 'index', 'outboundTag', 'protected', 'protectionReason', 'tag', 'type']
    );
    assert.ok(Array.isArray(rule.conditions));
    rule.conditions.forEach(condition => {
      assert.ok(conditionKeys.has(condition.key), `unexpected condition key: ${condition.key}`);
      assert.equal(typeof condition.value, 'string');
    });
  });

  status.routing.balancers.forEach(balancer => {
    assert.deepEqual(Object.keys(balancer).sort(), ['selector', 'strategy', 'tag']);
    assert.ok(Array.isArray(balancer.selector));
  });

  const serialized = JSON.stringify(status.routing).toLowerCase();
  for (const secretKey of ['privatekey', 'password', 'shortid', 'uuid']) {
    assert.equal(serialized.includes(secretKey), false, `routing response exposes ${secretKey}`);
  }
});

test('routing page contains the read-only rules and balancers views', async () => {
  const response = await fetch(`${baseUrl}/`);
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /<h3>Routing<\/h3>/);
  assert.match(html, /<h4>System rules<\/h4>/);
  assert.match(html, /userRoutingRules\(\)/);
  assert.match(html, /<h3>Balancers<\/h3>/);
  assert.match(html, /status\?\.routing\?\.domainStrategy/);
  assert.doesNotMatch(html, /raw json/i);
});

test('routing mutation is disabled when control mode is off', async () => {
  const response = await fetch(`${baseUrl}/api/routing/rules`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ rule: { conditions: { domain: 'example.com' }, outboundTag: 'direct' } })
  });
  assert.equal(response.status, 403);
  const result = await response.json();
  assert.match(result.message, /disabled/i);
});

test('read-only logs and geodata are available through the live helper', async () => {
  const logsResponse = await fetch(`${baseUrl}/api/logs/error?limit=500`);
  assert.equal(logsResponse.status, 200);
  const logs = await logsResponse.json();
  assert.equal(logs.ok, true);
  assert.equal(logs.kind, 'error');
  assert.ok(Array.isArray(logs.lines));
  assert.ok(logs.lines.length <= 200);

  const geodataResponse = await fetch(`${baseUrl}/api/settings/geodata`);
  assert.equal(geodataResponse.status, 200);
  const geodata = await geodataResponse.json();
  assert.equal(geodata.installed, true);
  assert.ok(Array.isArray(geodata.files));
  assert.deepEqual(geodata.files.map(file => file.name).sort(), ['geoip.dat', 'geosite.dat']);
  assert.ok(geodata.files.every(file => typeof file.exists === 'boolean'));
});
