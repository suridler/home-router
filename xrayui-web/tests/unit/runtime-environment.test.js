'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const configPath = require.resolve('../../config');
const helperPath = require.resolve('../../services/helper');

function environment() {
  const env = { ...process.env };
  delete env.XRAY_CORE_HELPER_SOCKET;
  delete env.XRAYUI_BOOTSTRAP_TOKEN;
  return env;
}

test('runtime environment supplies socket and setup token without a local config file', () => {
  const result = spawnSync(process.execPath, ['-e', `
    const assert = require('node:assert/strict');
    const config = require(${JSON.stringify(configPath)});
    assert.equal(config.helperSocket, '/fixture/helper.sock');
    assert.equal(config.bootstrapToken, 'fixture-token');
    require(${JSON.stringify(helperPath)});
  `], { env: { ...environment(), XRAY_CORE_HELPER_SOCKET: '/fixture/helper.sock', XRAYUI_BOOTSTRAP_TOKEN: 'fixture-token' }, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
});

test('local .env cannot supply missing runtime environment; setup token is optional', t => {
  const cwd = fs.mkdtempSync(path.join(os.tmpdir(), 'web-environment-'));
  t.after(() => fs.rmSync(cwd, { recursive: true, force: true }));
  fs.writeFileSync(path.join(cwd, '.env'), 'XRAY_CORE_HELPER_SOCKET=/fixture/ignored.sock\nXRAYUI_BOOTSTRAP_TOKEN=ignored-token\n');
  const result = spawnSync(process.execPath, ['-e', `
    const assert = require('node:assert/strict');
    const config = require(${JSON.stringify(configPath)});
    assert.equal(config.helperSocket, undefined);
    assert.equal(config.bootstrapToken, '');
    assert.throws(() => require(${JSON.stringify(helperPath)}), /socketPath must be an absolute path/);
  `], { cwd, env: environment(), encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
});
