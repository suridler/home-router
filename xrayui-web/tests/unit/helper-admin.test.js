'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createHelperAdminService } = require('../../services/helper-admin');
const { LONG_OPERATION_TIMEOUT_MS } = require('../../services/helper-client');

test('admin operations use only typed helper endpoints and payloads', async () => {
  const calls = [];
  const client = {
    async request(method, path, options = {}) {
      calls.push({ method, path, options });
      if (path === '/v1/proxy-domains') return method === 'GET' ? { desired: ['new.example'], baseline: ['old.example'], consolidationActive: false } : { domain: 'new.example', applied: true };
      if (path.startsWith('/v1/proxy-domains/')) return { domain: 'old.example', applied: true };
      if (path === '/v1/xray/config/validate') return { status: 'valid' };
      if (path.startsWith('/v1/xray/services/')) return { unit: 'xray', action: 'restart', state: 'active' };
      if (path === '/v1/xray/logging') return { generation: 4, contentHash: 'logs' };
      if (path === '/v1/xray/geodata/settings') return { generation: 3, settings: options.body.settings };
      if (path === '/v1/xray/geodata/update') return { operation: { state: 'succeeded' } };
      if (path === '/v1/templates') return { templates: [{ templateId: 'xray-config', state: 'different' }] };
      if (path === '/v1/templates/refresh') return { templates: [{ templateId: 'xray-config', state: 'synchronized' }] };
      if (path === '/v1/templates/nftables/backups') return { templateId: 'nftables', generation: 4, backups: [] };
      if (path === '/v1/templates/nftables/accept-system') return { templateId: 'nftables', accepted: true, generation: 5, revision: 'new', state: 'synchronized' };
      if (path === '/v1/settings') return method === 'GET' ? { backup: { retainPerTemplate: 10 } } : { backup: options.body.backup };
      throw new Error(`unexpected ${method} ${path}`);
    }
  };
  const service = createHelperAdminService({ helperClient: client });

  assert.equal((await service.getProxyDomains()).pendingAdditions[0], 'new.example');
  assert.equal((await service.addProxyDomain('new.example')).snapshot.pendingDeletions[0], 'old.example');
  await service.deleteProxyDomain('old.example');
  await service.validateConfiguration();
  await service.controlService('xray.service', 'restart');
  await service.updateLogSettings({ access: { mode: 'file' }, error: { mode: 'none' }, loglevel: 'warning', dnsLog: true, rotation: { enabled: true }, confirmRestrictedPaths: true }, true, 3);
  await service.saveGeodataSettings({ expectedGeneration: 2, geositeUrl: 'https://data.example/geosite.dat', geositeSha256Url: 'https://data.example/geosite.sha256', geoipUrl: 'https://data.example/geoip.dat', geoipSha256Url: 'https://data.example/geoip.sha256' });
  await service.updateGeodata();
  assert.equal((await service.getTemplateStatus()).templates[0].state, 'different');
  assert.equal((await service.refreshTemplateStatus()).templates[0].state, 'synchronized');
  assert.equal((await service.getTemplateBackups('nftables')).generation, 4);
  assert.equal((await service.acceptSystemSettings('nftables', 4, 'old')).accepted, true);
  assert.equal((await service.getSystemSettings()).backup.retainPerTemplate, 10);
  assert.equal((await service.updateSystemSettings(25)).backup.retainPerTemplate, 25);

  const acceptSystem = calls.find(call => call.path === '/v1/templates/nftables/accept-system');
  assert.deepEqual(acceptSystem.options.body, { expectedGeneration: 4, expectedRevision: 'old' });
  assert.equal(acceptSystem.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  const logging = calls.find(call => call.path === '/v1/xray/logging');
  assert.deepEqual(logging.options.body.access, { mode: 'file' });
  assert.deepEqual(logging.options.body.error, { mode: 'disabled' });
  assert.equal(logging.options.body.expectedGeneration, 3);
  assert.equal(logging.options.body.confirmRestrictedPaths, true);
  assert.equal(logging.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  const validation = calls.find(call => call.path === '/v1/xray/config/validate');
  assert.equal(validation.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  const serviceAction = calls.find(call => call.path === '/v1/xray/services/xray/restart');
  assert.equal(serviceAction.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);
  const update = calls.find(call => call.path === '/v1/xray/geodata/update');
  assert.match(update.options.headers['Idempotency-Key'], /^xrayui-/);
  assert.equal(update.options.timeoutMs, undefined);
  assert.equal(calls.some(call => call.path.includes('systemctl') || call.path.includes('config.json')), false);
});

test('geodata settings preserve explicit empty origins and never submit an executable', async () => {
  let payload;
  const service = createHelperAdminService({ helperClient: {
    async request(method, path, options) {
      assert.equal(method, 'PUT');
      assert.equal(path, '/v1/xray/geodata/settings');
      payload = options.body;
      return payload;
    }
  } });
  await service.saveGeodataSettings({ expectedGeneration: 42, originsText: '', maxAssetSizeMB: 512,
    userGeosite: 'geosite_user.dat', userGeoip: 'geoip_user.dat', enabled: false, time: '05:30',
    builder: '/must/not/execute', geositeUrl: 'https://data.example/geosite.dat', geositeSha256Url: 'https://data.example/geosite.sha256',
    geoipUrl: 'https://data.example/geoip.dat', geoipSha256Url: 'https://data.example/geoip.sha256' });
  assert.deepEqual(payload.settings.origins, []);
  assert.deepEqual(payload.settings.schedule, { enabled: false, time: '05:30' });
  assert.equal(payload.settings.maxAssetSizeMB, 512);
  assert.equal(payload.settings.userGeoip, 'geoip_user.dat');
  assert.equal(Object.hasOwn(payload.settings, 'builder'), false);
  assert.equal(payload.expectedGeneration, 42);
});
