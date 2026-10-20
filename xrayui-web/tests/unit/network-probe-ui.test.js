'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_SOURCE = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');

function deadlineIn(seconds = 300) {
  return new Date(Date.now() + seconds * 1000).toISOString();
}

function appFixture() {
  let factory;
  vm.runInNewContext(APP_SOURCE, {
    window: { confirm: () => true },
    document: { addEventListener: (_name, callback) => callback() },
    Alpine: { data: (_name, create) => { factory = create; } },
  });
  const app = factory();
  app.status = { controlEnabled: true };
  app.safeApplyKnown = true;
  return app;
}

test('TProxy activation probes the baseline, applies once, then probes the returned transaction', async () => {
  const app = appFixture();
  const calls = [];
  const transactionId = 'tx-network-1';

  app.loadTemplates = async () => {
    app.templates = [{ templateId: 'tproxy-unit', state: 'synchronized', summary: {} }];
  };
  app.api = async (requestPath, options = {}) => {
    const call = {
      requestPath,
      method: options.method || 'GET',
      body: options.body ? JSON.parse(options.body) : null,
    };
    calls.push(call);
    if (requestPath === '/api/network/probe') {
      return { reachable: true, checkedAt: new Date().toISOString(), transactionId: call.body.transactionId };
    }
    if (requestPath === '/api/templates/tproxy-unit/backups') {
      return { completedGeneration: 4, backups: [] };
    }
    if (requestPath === '/api/templates/tproxy-unit/apply') {
      return {
        transactionId,
        templateId: 'tproxy-unit',
        deadline: deadlineIn(),
        phase: 'applied',
        state: 'pending',
        generation: 5,
      };
    }
    throw new Error(`unexpected request: ${call.method} ${requestPath}`);
  };

  await app.activateNetwork();

  const probes = calls.filter(call => call.requestPath === '/api/network/probe');
  const applies = calls.filter(call => call.requestPath === '/api/templates/tproxy-unit/apply');

  assert.equal(applies.length, 1, 'activation must issue exactly one apply');
  assert.equal(probes.length, 2, 'activation must probe before and after applying');
  assert.deepEqual(probes.map(call => call.body.stage), ['before', 'after']);
  assert.equal(probes[0].body.transactionId, undefined);
  assert.equal(probes[1].body.transactionId, transactionId);
  assert.ok(calls.indexOf(probes[0]) < calls.indexOf(applies[0]), 'baseline probe must precede apply');
  assert.ok(calls.indexOf(applies[0]) < calls.indexOf(probes[1]), 'post-apply probe must follow apply');
  assert.deepEqual(applies[0].body, { expectedGeneration: 4 });
  assert.equal(app.safeApply.transactionId, transactionId);
  assert.equal(app.safeApply.templateId, 'tproxy-unit');
  assert.equal(app.networkProbes.before.reachable, true);
  assert.equal(app.networkProbes.after.transactionId, transactionId);
  assert.equal(app.pendingAction, '');
});

test('an unreachable HTTPS probe neither auto-confirms nor blocks explicit confirmation', async () => {
  const app = appFixture();
  const calls = [];
  const transactionId = 'tx-unreachable';

  app.loadTemplates = async () => {};
  app.api = async (requestPath, options = {}) => {
    const call = {
      requestPath,
      method: options.method || 'GET',
      body: options.body ? JSON.parse(options.body) : null,
    };
    calls.push(call);
    if (requestPath === '/api/network/probe') {
      return {
        reachable: false,
        transactionId: call.body.transactionId,
        https: { status: 'failed', code: 'timeout' },
      };
    }
    if (requestPath === '/api/templates/tproxy-unit/backups') {
      return { completedGeneration: 2, backups: [] };
    }
    if (requestPath === '/api/templates/tproxy-unit/apply') {
      return {
        transactionId,
        templateId: 'tproxy-unit',
        deadline: deadlineIn(),
        phase: 'applied',
        state: 'pending',
        generation: 3,
      };
    }
    if (requestPath === `/api/safe-apply/${transactionId}/confirm` && call.method === 'POST') {
      return { transaction: { transactionId, templateId: 'tproxy-unit' } };
    }
    throw new Error(`unexpected request: ${call.method} ${requestPath}`);
  };

  await app.applyTemplateSettings({ templateId: 'tproxy-unit', state: 'different', summary: {} }, true);

  const automaticResolutions = calls.filter(call => /\/api\/safe-apply\/[^/]+\/(confirm|rollback)$/.test(call.requestPath));
  assert.equal(automaticResolutions.length, 0, 'a failed probe must not resolve safe apply');
  assert.equal(app.safeApply.transactionId, transactionId);
  assert.equal(app.networkProbes.after.reachable, false);

  await app.resolveSafeApply('confirm');

  const confirmations = calls.filter(call => call.requestPath === `/api/safe-apply/${transactionId}/confirm`);
  assert.equal(confirmations.length, 1);
  assert.equal(confirmations[0].method, 'POST');
  assert.equal(app.safeApply, null);
  assert.match(app.templateMessage, /настройки подтверждены/);
});

test('retrying the network probe sends only a probe and never reapplies the template', async () => {
  const app = appFixture();
  const calls = [];
  const transactionId = 'tx-retry';

  app.safeApply = {
    transactionId,
    templateId: 'tproxy-unit',
    deadline: deadlineIn(),
    phase: 'applied',
  };
  app.api = async (requestPath, options = {}) => {
    const call = {
      requestPath,
      method: options.method || 'GET',
      body: options.body ? JSON.parse(options.body) : null,
    };
    calls.push(call);
    if (requestPath === '/api/network/probe') {
      return { reachable: false, transactionId: call.body.transactionId };
    }
    throw new Error(`unexpected request: ${call.method} ${requestPath}`);
  };

  await app.runNetworkProbe('after', transactionId);
  await app.runNetworkProbe();

  assert.deepEqual(
    calls.map(call => [call.method, call.requestPath, call.body.stage]),
    [
      ['POST', '/api/network/probe', 'after'],
      ['POST', '/api/network/probe', 'manual'],
    ],
  );
  assert.equal(calls[0].body.transactionId, transactionId);
  assert.equal(app.safeApply.transactionId, transactionId, 'retrying a probe must not touch the pending transaction');
  assert.equal(app.networkProbes.after.reachable, false);
});

test('a lost apply response does not retry apply and reloads the pending safe-apply status', async () => {
  const app = appFixture();
  const calls = [];
  const pending = {
    transactionId: 'tx-lost-response',
    templateId: 'nftables',
    deadline: deadlineIn(),
    phase: 'applied',
  };
  const lostResponse = new Error('response lost after apply');
  lostResponse.code = 'response_lost';

  app.api = async (requestPath, options = {}) => {
    const call = {
      requestPath,
      method: options.method || 'GET',
      body: options.body ? JSON.parse(options.body) : null,
    };
    calls.push(call);
    if (requestPath === '/api/templates/nftables/backups') {
      return { completedGeneration: 9, backups: [] };
    }
    if (requestPath === '/api/templates/nftables/apply') throw lostResponse;
    if (requestPath === '/api/safe-apply') return { pending };
    throw new Error(`unexpected request: ${call.method} ${requestPath}`);
  };

  await app.applyTemplateSettings({ templateId: 'nftables', state: 'different', summary: {} });

  const applies = calls.filter(call => call.requestPath === '/api/templates/nftables/apply');
  const statusLoads = calls.filter(call => call.requestPath === '/api/safe-apply');

  assert.equal(applies.length, 1, 'a lost response must not cause another apply');
  assert.deepEqual(applies[0].body, { expectedGeneration: 9 });
  assert.equal(statusLoads.length, 1);
  assert.equal(statusLoads[0].method, 'GET');
  assert.deepEqual(app.safeApply, pending, 'the pending transaction must be restored from shared status');
  assert.equal(app.safeApplyKnown, true);
  assert.equal(app.pendingAction, '');
  assert.match(app.templateMessage, /response lost after apply/);
});

test('saving system settings forwards connectivity URL, expected status and timeout', async () => {
  const app = appFixture();
  const calls = [];
  const connectivity = {
    url: 'https://probe.example.test/health',
    expectedStatus: 200,
    timeoutSeconds: 9,
  };

  app.systemSettings.backup.retainPerTemplate = 12;
  app.systemSettings.connectivity = connectivity;
  app.api = async (requestPath, options = {}) => {
    const call = {
      requestPath,
      method: options.method || 'GET',
      body: options.body ? JSON.parse(options.body) : null,
    };
    calls.push(call);
    if (requestPath === '/api/settings/system' && call.method === 'PUT') {
      return { backup: call.body.backup, connectivity: call.body.connectivity };
    }
    throw new Error(`unexpected request: ${call.method} ${requestPath}`);
  };

  await app.saveSystemSettings();

  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, 'PUT');
  assert.equal(calls[0].requestPath, '/api/settings/system');
  assert.deepEqual(calls[0].body.connectivity, connectivity);
  assert.equal(calls[0].body.backup.retainPerTemplate, 12);
  assert.equal(app.settingsMessage, 'Настройки helper сохранены.');
});
