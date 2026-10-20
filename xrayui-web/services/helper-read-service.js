'use strict';


const { gatewayStatus } = require('./gateway-status');

function createHelperReadService({ helperClient, controlEnabled, trafficReader }) {
  if (!helperClient || typeof helperClient.request !== 'function') throw new TypeError('helperClient is required');
  if (typeof trafficReader !== 'function') throw new TypeError('trafficReader is required');

  async function readLog(kind, limit) {
    return helperClient.request('GET', `/v1/logs/${encodeURIComponent(kind)}?limit=${limit}`);
  }

  async function getGeodataStatus() {
    const [assets, versioned, update] = await Promise.all([
      helperClient.request('GET', '/v1/xray/geodata'),
      helperClient.request('GET', '/v1/xray/geodata/settings'),
      helperClient.request('GET', '/v1/xray/geodata/update/status')
    ]);
    const settings = versioned.settings || {};
    const sourcesConfigured = Array.isArray(settings.origins) && settings.origins.length > 0;
    const files = (assets.files || []).map(file => ({
      name: file.name,
      path: file.path,
      exists: file.present === true,
      size: file.size || 0,
      modifiedAt: file.modifiedAt || null
    }));
    const missing = files.some(file => !file.exists);
    const recoveryBlocked = update.recovery?.mutationsBlocked === true;
    const failedOperation = update.operation?.state === 'failed';
    const sizeLimit = update.operation?.errorCode === 'geodata_asset_too_large' && Number.isSafeInteger(update.maxAssetSizeMB) && update.maxAssetSizeMB > 0;
    const failureLabel = sizeLimit ? `Размер файла геобазы превышает заданный лимит — ${update.maxAssetSizeMB} МБ` : 'Последнее обновление геоданных завершилось ошибкой';
    const state = recoveryBlocked || failedOperation || missing ? 'error' : sourcesConfigured ? 'ok' : 'disabled';
    const label = recoveryBlocked ? 'Требуется восстановление геоданных'
      : failedOperation ? failureLabel
        : missing ? 'Отсутствуют файлы геоданных'
          : sourcesConfigured ? 'Обновление управляется helper' : 'Автообновление геоданных не настроено';
    const modified = files.map(file => Date.parse(file.modifiedAt)).filter(Number.isFinite);

    return {
      settings: {
        enabled: sourcesConfigured,
        time: '04:00',
        assetDir: assets.assetDir,
        geositeUrl: settings.geosite?.url || '',
        geositeSha256Url: settings.geosite?.checksum?.url || '',
        geoipUrl: settings.geoip?.url || '',
        geoipSha256Url: settings.geoip?.checksum?.url || '',
        expectedGeneration: versioned.generation
      },
      settingsGeneration: versioned.generation,
      helperSettings: settings,
      files,
      installed: true,
      timer: { active: sourcesConfigured, enabled: sourcesConfigured, managedBy: 'xray-core-helper' },
      operation: update.operation || null,
      recovery: update.recovery,
      health: {
        state,
        label,
        lastUpdatedAt: modified.length ? new Date(Math.min(...modified)).toISOString() : null
      }
    };
  }

  const observe = (path, fallback) => helperClient.request('GET', path).catch(() => ({ ...fallback, unavailable: true }));

  async function getRouterStatus() {
    const [readiness, runtime, status, inbounds, outbounds, routing, logging, diagnostics, geodataStatus, helperHealth, setupState, errorHistory] = await Promise.all([
      helperClient.request('GET', '/v1/ready', { acceptStatuses: [503] }),
      observe('/v1/xray/runtime', {}),
      observe('/v1/xray/status', {}),
      observe('/v1/xray/inbounds', {}),
      observe('/v1/xray/outbounds', {}),
      observe('/v1/xray/routing', {}),
      observe('/v1/xray/logging', {}),
      observe('/v1/xray/diagnostics', {}),
      getGeodataStatus().catch(() => ({ files: [], health: { state: 'unknown', label: 'Состояние геоданных недоступно' } })),
      helperClient.request('GET', '/v1/health').catch(() => null),
      helperClient.request('GET', '/v1/setup').catch(() => null),
      helperClient.request('GET', '/v1/errors?limit=5').catch(() => ({ errors: [], unavailable: true }))
    ]);
    const checks = new Map((diagnostics.checks || []).map(check => [check.name, check.status]));
    const xrayIsActive = checks.get('xray_service_active') === 'ok';
    const systemOK = checks.get('tproxy_service_active') === 'ok' && (diagnostics.checks || []).every(check => ['ok', 'unchecked', 'not_declared'].includes(check.status));
    const configOK = status.configReadable === true && status.configParsed === true && status.configDrift !== true;
    const gateway = gatewayStatus({ readiness, helperHealth, status, diagnostics, geodata: geodataStatus, setup: setupState });
    const overallState = gateway.state;
    const trafficStatus = await trafficReader({
      inbounds: inbounds.inbounds || [],
      outbounds: outbounds.outbounds || []
    }, runtime.api, runtime.binary);

    const networkProfile = {
      gateway: null, upstream: null, subnet: setupState?.operation?.lanNetwork || null,
      interface: setupState?.operation?.networkInterface || null,
      fwmark: null, table: null,
      mode: checks.get('tproxy_service_active') === 'ok' ? 'tproxy-service-active' : 'direct-or-unconfigured',
      xrayUnit: 'xray.service', nftTable: null
    };
    return {
      status: overallState === 'ok' ? 'ready' : 'degraded',
      xray: xrayIsActive ? 'running' : checks.get('xray_service_active') === 'mismatch' ? 'stopped' : 'unavailable',
      gateway: networkProfile.gateway,
      upstream: networkProfile.upstream,
      subnet: networkProfile.subnet,
      fwmark: networkProfile.fwmark,
      table: networkProfile.table,
      mode: networkProfile.mode,
      uptime: 'managed by systemd',
      traffic: trafficStatus.traffic || { in: '—', out: '—' },
      trafficRates: trafficStatus.rates || null,
      trafficSource: trafficStatus.source,
      trafficError: trafficStatus.error || (!trafficStatus.rates ? 'Traffic data unavailable' : null),
      xrayBinary: runtime.binary,
      xrayBinarySource: 'helper',
      xrayApi: runtime.api,
      xrayApiSource: 'helper',
      xrayConfig: runtime.config,
      xrayConfigSource: 'helper',
      ports: inbounds.inbounds || [],
      outboundTags: outbounds.outbounds || [],
      proxyState: (outbounds.outbounds || []).some(item => item.tag === 'proxy' && item.protocol === 'freedom')
        ? 'direct-placeholder' : (outbounds.outbounds || []).some(item => item.tag === 'proxy') ? 'configured' : 'missing',
      routing: {
        ...routing,
        rules: (routing.rules || []).map(rule => rule.tag ? rule : {
          ...rule,
          protected: true,
          protectionReason: rule.protectionReason || 'Rule has no stable ruleTag'
        })
      },
      logs: { access: logging.access, error: logging.error, loglevel: logging.loglevel, dnsLog: logging.dnsLog },
      configState: status.configState,
      configError: configOK ? null : status.configError || status.configState || 'Xray config is unavailable',
      geodata: geodataStatus,
      dashboard: {
        state: overallState,
        label: overallState === 'ok' ? 'Локальные проверки пройдены' : overallState === 'warning' ? 'Часть состояния не подтверждена' : 'Обнаружены проблемы',
        updatedAt: new Date().toISOString(),
        installation: {
          helperVersion: helperHealth?.version || null,
          setup: !setupState ? 'unavailable' : setupState.recoveryRequired ? 'recovery' : setupState.setupRequired ? 'required' : 'completed',
          networkPrepared: setupState?.operation?.networkPrepared === true,
          interface: setupState?.operation?.networkInterface || null,
          subnet: setupState?.operation?.lanNetwork || null,
          tproxyService: checks.get('tproxy_service_active') || 'unavailable',
          forwarding: 'unverified'
        },
        components: gateway.components,
        issues: gateway.issues
      },
      route: checks.get('default_route') === 'ok' ? 'configured' : 'unavailable',
      policyRule: checks.get('policy_rule') === 'ok' ? 'configured' : 'unavailable',
      policyRoute: checks.get('policy_route') === 'ok' ? 'configured' : 'unavailable',
      tproxy: { ok: systemOK, nft: 'unchecked' },
      nft: checks.get('nft_runtime') === 'ok' ? 'configured' : 'missing',
      xrayUnit: xrayIsActive ? 'active' : 'inactive',
      lastLogs: 'available through helper',
      errorHistory,
      controlEnabled,
      config: networkProfile,
      generation: status.generation,
      contentHash: status.contentHash
    };
  }

  async function getErrorHistory() {
    return helperClient.request('GET', '/v1/errors?limit=100');
  }

  return { getGeodataStatus, getRouterStatus, getErrorHistory, readLog };
}

module.exports = { createHelperReadService };
