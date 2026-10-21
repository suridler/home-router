'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createApp } = require('../../app');
const { createBackend } = require('../../services/backend');
const { createHelperClient } = require('../../services/helper-client');
const { startFakeHelper, writeJSON } = require('../helpers/fake-helper');

const metadata = { generation: 7, contentHash: 'hash', modifiedAt: '2026-10-08T12:00:00Z' };

function helperPayload(pathname) {
  const path = new URL(pathname, 'http://helper').pathname;
  const responses = {
    '/v1/ready': { status: 'ready', checks: [] },
    '/v1/xray/runtime': { binary: '/unreadable/xray', config: '/unreadable/config.json', api: '127.0.0.1:10085', assetDir: '/unreadable/assets' },
    '/v1/xray/status': { ...metadata, configState: 'parsed', configReadable: true, configParsed: true, configDrift: false, configError: '' },
    '/v1/xray/inbounds': { ...metadata, inbounds: [{ tag: 'tproxy-in', listen: '127.0.0.1', port: 52345, protocol: 'dokodemo-door' }] },
    '/v1/xray/outbounds': { ...metadata, outbounds: [{ tag: 'proxy', protocol: 'vless' }] },
    '/v1/xray/routing': { ...metadata, domainStrategy: 'IPIfNonMatch', rules: [], balancers: [] },
    '/v1/xray/logging': { ...metadata, access: '/var/log/xray/access.log', error: '', loglevel: 'warning', dnsLog: true },
    '/v1/xray/diagnostics': { checks: [
      { name: 'default_route', status: 'ok' },
      { name: 'policy_rule', status: 'ok' },
      { name: 'policy_route', status: 'ok' },
      { name: 'nft_runtime', status: 'ok' },
      { name: 'native_relationships', status: 'ok' },
      { name: 'xray_service_active', status: 'ok' },
      { name: 'tproxy_service_active', status: 'ok' }
    ] },
    '/v1/xray/geodata': { ...metadata, assetDir: '/unreadable/assets', files: [
      { name: 'geosite.dat', path: '/unreadable/assets/geosite.dat', present: true, size: 10, modifiedAt: '2026-10-08T12:00:00Z' },
      { name: 'geoip.dat', path: '/unreadable/assets/geoip.dat', present: true, size: 20, modifiedAt: '2026-10-08T12:00:00Z' }
    ] },
    '/v1/xray/geodata/settings': { generation: 2, settings: { origins: [], geosite: { url: '', checksum: { url: '', algorithm: 'sha256' } }, geoip: { url: '', checksum: { url: '', algorithm: 'sha256' } } } },
    '/v1/xray/geodata/update/status': { settings: { generation: 2, settings: {} }, recovery: { phase: 'none', code: 'recovery_clear', mutationsBlocked: false } },
    '/v1/errors': { errors: [{ id: 9, time: '2026-10-18T12:00:00Z', source: 'journal', message: '[Error] retained' }], capacity: 100, collectedAt: '2026-10-18T12:00:01Z' },
    '/v1/logs/error': { kind: 'error', source: 'journal', lines: ['sanitized helper line'], bytesRead: 21, byteLimit: 1048576, lineLimit: 3, truncated: false }
  };
  return responses[path];
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

test('read-only web APIs use the fake helper when host config paths are unreadable', async t => {
  const calls = [];
  const fake = await startFakeHelper((request, response) => {
    calls.push(request.url);
    const payload = helperPayload(request.url);
    if (!payload) return writeJSON(response, 404, { error: { code: 'not_found', message: 'not found' } });
    writeJSON(response, 200, payload);
  });
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath });
  const services = createBackend({
    client,
    trafficReader: async () => ({ traffic: { in: '0 B/s', out: '0 B/s' }, rates: { in: 0, out: 0 }, source: 'test', error: null })
  });
  const web = await startWeb(createApp(services));
  t.after(() => web.close());

  const statusResponse = await fetch(`${web.baseURL}/api/status`);
  assert.equal(statusResponse.status, 200);
  const status = await statusResponse.json();
  assert.equal(status.xrayConfig, '/unreadable/config.json');
  assert.equal(status.xrayConfigSource, 'helper');
  assert.equal(status.routing.domainStrategy, 'IPIfNonMatch');
  assert.equal(status.errorHistory.errors[0].id, 9);
  assert.equal(status.errorHistory.capacity, 100);

  const logsResponse = await fetch(`${web.baseURL}/api/logs/error?limit=200`);
  assert.equal(logsResponse.status, 200);
  assert.deepEqual((await logsResponse.json()).lines, ['sanitized helper line']);

  const geodataResponse = await fetch(`${web.baseURL}/api/settings/geodata`);
  assert.equal(geodataResponse.status, 200);
  const geodata = await geodataResponse.json();
  assert.equal(geodata.files.length, 2);
  assert.equal(geodata.files[0].exists, true);
  assert.ok(calls.includes('/v1/xray/status'));
  assert.ok(calls.includes('/v1/xray/routing'));
  assert.ok(calls.includes('/v1/logs/error?limit=200'));
  assert.ok(calls.includes('/v1/xray/geodata/update/status'));
  assert.equal(calls.includes('/v1/xray/geodata/settings'), false);
});

test('dashboard reports onboarding evidence and keeps unavailable traffic distinct from zero', async () => {
  const { createHelperReadService } = require('../../services/helper-read-service');
  const service = createHelperReadService({
    controlEnabled: false,
    helperClient: { async request(method, path) {
      if (path === '/v1/health') return { status: 'ok', version: 'test-version' };
      if (path === '/v1/setup') return { setupRequired: false, operation: { networkPrepared: true, networkInterface: 'lan7', lanNetwork: '10.7.0.0/24' } };
      return helperPayload(path);
    } },
    trafficReader: async () => ({ error: 'exporter_unavailable' })
  });
  const status = await service.getRouterStatus();
  assert.equal(status.trafficRates, null);
  assert.equal(status.traffic.in, '—');
  assert.equal(status.dashboard.installation.interface, 'lan7');
  assert.equal(status.dashboard.installation.subnet, '10.7.0.0/24');
  assert.equal(status.dashboard.installation.helperVersion, 'test-version');
  assert.equal(status.dashboard.installation.forwarding, 'unverified');
});

test('a freedom outbound named proxy is reported as an initial direct placeholder', async () => {
  const { createHelperReadService } = require('../../services/helper-read-service');
  const service = createHelperReadService({ controlEnabled: false, trafficReader: async () => ({}),
    helperClient: { async request(method, path) {
      if (path === '/v1/xray/outbounds') return { outbounds: [{ tag: 'proxy', protocol: 'freedom' }, { tag: 'direct', protocol: 'freedom' }] };
      return helperPayload(path);
    } }
  });
  assert.equal((await service.getRouterStatus()).proxyState, 'direct-placeholder');
});

test('partial diagnostic outage keeps the dashboard available with unknown observations', async () => {
  const { createHelperReadService } = require('../../services/helper-read-service');
  const service = createHelperReadService({controlEnabled:false, trafficReader:async()=>({}),helperClient:{async request(_,path){
    if (path==='/v1/xray/diagnostics' || path==='/v1/xray/geodata') throw Error('offline');
    if (path==='/v1/health')return{status:'ok'};
    if (path==='/v1/setup')return{setupRequired:false};
    return helperPayload(path);
  }}});
  const result = await service.getRouterStatus();
  assert.equal(result.xray,'unavailable');
  assert.equal(result.dashboard.state,'warning');
  assert.equal(result.dashboard.components.find(c=>c.key==='system').state,'unknown');
  assert.equal(result.dashboard.components.find(c=>c.key==='geodata').state,'unknown');
});

test('geodata size failure shows the configured limit on the dashboard', async () => {
  const { createHelperReadService } = require('../../services/helper-read-service');
  const service = createHelperReadService({ controlEnabled: false, trafficReader: async () => ({}),
    helperClient: { async request(_, path) {
      if (path === '/v1/xray/geodata/update/status') return {
        maxAssetSizeMB: 512, operation: { state: 'failed', errorCode: 'geodata_asset_too_large' },
        recovery: { mutationsBlocked: false }
      };
      return helperPayload(path);
    } }
  });
  const result = await service.getGeodataStatus();
  assert.equal(result.health.state, 'error');
  assert.equal(result.health.label, 'Размер файла геобазы превышает заданный лимит — 512 МБ');
});

test('geodata partial failure shows its stage and a successful retry clears the current error', async () => {
  const { createHelperReadService } = require('../../services/helper-read-service');
  const issue = { process: 'external', file: 'geosite.dat', stage: 'checksum', message: 'Контрольная сумма SHA256 не совпадает', code: 'geodata_external_checksum_failed', at: 1000 };
  let operation = { id: 'op-1', state: 'failed', report: { issues: [issue], files: [{ file: 'geoip.dat', state: 'replaced' }] } };
  const service = createHelperReadService({ controlEnabled: false, trafficReader: async () => ({}), helperClient: {
    async request(_, path) {
      if (path === '/v1/xray/geodata/settings') throw Error('redundant settings read');
      if (path === '/v1/xray/geodata/update/status') return {
        settings: { generation: 23, settings: { origins: [], builder: '/trusted/builder', schedule: { enabled: true, time: '04:00' } } },
        lastSuccessfulAt: 2000, nextScheduledAt: 3000, operation
      };
      return helperPayload(path);
    }
  } });
  let result = await service.getGeodataStatus();
  assert.equal(result.health.state, 'error');
  assert.match(result.health.label, /geosite.dat.*SHA256/);
  assert.equal(result.operation.report.files[0].state, 'replaced');
  assert.equal(result.health.lastUpdatedAt, new Date(2000).toISOString());
  assert.equal(result.nextScheduledAt, 3000);
  assert.equal(result.settings.builder, '/trusted/builder');
  assert.equal(result.settings.expectedGeneration, 23);
  operation = { id: 'op-2', state: 'succeeded', report: { issues: [], files: [] } };
  result = await service.getGeodataStatus();
  assert.equal(result.health.state, 'ok');
  assert.equal(result.operation.id, 'op-2');
});
