'use strict';
const assert = require('node:assert/strict');
const { test } = require('node:test');
const { EventEmitter } = require('node:events');
const config = require('../../config');
const { start } = require('../../server');

test('web fetches helper policy before loading app or binding, ignoring managed env', async () => {
  process.env.HOST = '0.0.0.0'; process.env.PORT = '9999'; process.env.XRAYUI_CONTROL_ENABLED = '1';
  const events = [];
  const result = new EventEmitter();
  const server = await start({
    client: { async request(method, path) { assert.equal(method, 'GET'); assert.equal(path, '/v1/web/settings'); events.push('settings'); return { enabled: true, host: '127.0.0.1', port: 3001, controlEnabled: false }; } },
    loadApp() {
      events.push('app'); assert.equal(config.controlEnabled, false);
      return { app: { listen(port, host, ready) { events.push('listen'); assert.equal(port, 3001); assert.equal(host, '127.0.0.1'); queueMicrotask(ready); return result; } }, startErrorMonitor() { events.push('monitor'); } };
    }
  });
  assert.equal(server, result); assert.deepEqual(events, ['settings', 'app', 'listen']);
});

test('no listener or app on missing, disabled, malformed or failed helper settings', async () => {
  const good = { enabled: true, host: '127.0.0.1', port: 3000, controlEnabled: false };
  for (const value of [null, {}, { ...good, enabled: false }, { ...good, host: '0.0.0.0' }, { ...good, host: 'localhost' }, { ...good, port: 0 }, { ...good, port: '3000' }, { ...good, controlEnabled: 'true' }]) {
    await assert.rejects(start({ client: { async request() { return value; } }, loadApp() { assert.fail('app loaded without valid settings'); } }));
  }
  await assert.rejects(start({ client: { async request() { throw new Error('helper unavailable'); } }, loadApp() { assert.fail('app loaded during outage'); } }));
});

test('listener failure propagates to the caller', async () => {
  const listener = new EventEmitter();
  await assert.rejects(start({ client: { async request() { return { enabled: true, host: '127.0.0.1', port: 3000, controlEnabled: true }; } }, loadApp() {
    return { app: { listen() { queueMicrotask(() => listener.emit('error', new Error('bind failed'))); return listener; } }, startErrorMonitor() { assert.fail('monitor started after bind failure'); } };
  } }), /bind failed/);
});
