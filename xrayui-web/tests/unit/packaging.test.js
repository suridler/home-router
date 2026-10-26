'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');

const root = path.resolve(__dirname, '../..');
const read = relativePath => fs.readFileSync(path.join(root, relativePath), 'utf8');

test('systemd unit runs only as the unprivileged helper socket consumer', () => {
  const unit = read('xrayui-web.service');
  assert.match(unit, /^User=xrayui-web$/m);
  assert.match(unit, /^Group=xrayui-web$/m);
  assert.match(unit, /^SupplementaryGroups=xray-core-helper$/m);
  assert.match(unit, /^NoNewPrivileges=true$/m);
  assert.match(unit, /^ProtectSystem=strict$/m);
  assert.match(unit, /^ProtectHome=true$/m);
  assert.match(unit, /^CapabilityBoundingSet=$/m);
  assert.match(unit, /^AmbientCapabilities=$/m);
  assert.doesNotMatch(unit, /^User=root$|^Group=root$|^ReadWritePaths=/m);
  assert.match(unit, /^EnvironmentFile=\/run\/xray-core-helper\/web\.env$/m);
  assert.match(unit, /^Requires=xray-core-helper\.service$/m);
  assert.doesNotMatch(unit, /^WantedBy=|^PartOf=|^BindsTo=/m);
  assert.doesNotMatch(unit, /^Wants=.*xray-core-helper/m);
});

test('deployment creates a dedicated account and verifies the API boundary', () => {
  const sysusers = read('packaging/sysusers.d/xrayui-web.conf');
  const smoke = read('packaging/install-and-smoke.sh');
  assert.match(sysusers, /^u xrayui-web /m);
  assert.match(smoke, /install-bundle\.sh/);
  assert.match(smoke, /\/v1\/web\/settings/);
  assert.doesNotMatch(smoke, /systemctl enable.*xrayui-web/);
  assert.match(smoke, /\/api\/status/);
  assert.match(smoke, /\/api\/network\/start/);
  assert.doesNotMatch(smoke, /\/api\/xray\/(?:stop|restart)/);
});
