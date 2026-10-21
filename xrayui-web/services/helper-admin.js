'use strict';

const crypto = require('node:crypto');
const { HelperClientError, LONG_OPERATION_TIMEOUT_MS } = require('./helper-client');

const persistenceUncertainCodes = new Set([
  'domain_runtime_sync_failed',
  'domain_runtime_apply_failed',
  'proxy_domain_unavailable',
  'helper_timeout',
  'helper_unavailable',
  'helper_invalid_response',
  'helper_response_too_large'
]);

function invalidHelperResponse(message) {
  return new HelperClientError(message, { code: 'helper_invalid_response' });
}

function proxyDomainsSnapshot(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)
    || !Array.isArray(data.desired) || data.desired.some(domain => typeof domain !== 'string')
    || !Array.isArray(data.baseline) || data.baseline.some(domain => typeof domain !== 'string')
    || typeof data.consolidationActive !== 'boolean') {
    throw invalidHelperResponse('Helper returned an invalid proxy-domain snapshot');
  }
  return data;
}

function proxyDomainMutation(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)
    || typeof data.domain !== 'string'
    || typeof data.applied !== 'boolean') {
    throw invalidHelperResponse('Helper returned an invalid proxy-domain mutation');
  }
  return data;
}

function mutationMayHavePersisted(error) {
  return persistenceUncertainCodes.has(error?.code)
    || (Number.isInteger(error?.statusCode) && error.statusCode >= 500);
}

function recordPersistenceUncertainty(error, uncertain) {
  if (error.mutationMayHavePersisted !== true) error.mutationMayHavePersisted = uncertain === true;
}

function createHelperAdminService({ helperClient }) {
  const request = (method, path, body, options = {}) => helperClient.request(method, path, { ...options, body });

  async function getProxyDomains() {
    const data = proxyDomainsSnapshot(await helperClient.request('GET', '/v1/proxy-domains'));
    const desired = data.desired;
    const baseline = data.baseline;
    return {
      permanentDomains: baseline,
      pendingAdditions: desired.filter(domain => !baseline.includes(domain)),
      pendingDeletions: baseline.filter(domain => !desired.includes(domain)),
      effectiveDomains: desired,
      runtime: { state: data.consolidationActive ? 'consolidating' : 'active' },
      consolidationActive: data.consolidationActive
    };
  }

  async function domainMutation(method, path, domain) {
    let mutation;
    try {
      mutation = await request(method, path, method === 'POST' ? { domain } : undefined, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
    } catch (error) {
      recordPersistenceUncertainty(error, mutationMayHavePersisted(error));
      throw error;
    }

    let canonical;
    try {
      canonical = proxyDomainMutation(mutation);
    } catch (error) {
      recordPersistenceUncertainty(error, true);
      throw error;
    }

    let snapshot;
    try {
      snapshot = await getProxyDomains();
    } catch (error) {
      recordPersistenceUncertainty(error, true);
      throw error;
    }

    return { domain: canonical.domain, applied: canonical.applied, snapshot };
  }

  const addProxyDomain = domain => domainMutation('POST', '/v1/proxy-domains', domain);
  const deleteProxyDomain = domain => domainMutation('DELETE', `/v1/proxy-domains/${encodeURIComponent(domain)}`, domain);

  async function validateConfiguration() {
    return request('POST', '/v1/xray/config/validate', {}, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  }

  async function controlService(unit, action) {
    const helperUnit = unit === 'xray.service' ? 'xray' : unit === 'xray-tproxy-routing.service' ? 'tproxy' : '';
    if (!helperUnit) {
      const error = new Error('unsupported service unit');
      error.statusCode = 400;
      error.code = 'invalid_service_action';
      throw error;
    }
    return request('POST', `/v1/xray/services/${helperUnit}/${encodeURIComponent(action)}`, {}, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  }

  async function updateLogSettings(settings, restart, expectedGeneration) {
    const target = value => ({ mode: value?.mode === 'none' ? 'disabled' : value?.mode || 'journal' });
    return request('PUT', '/v1/xray/logging', {
      access: target(settings.access),
      error: target(settings.error),
      loglevel: settings.loglevel,
      dnsLog: settings.dnsLog,
      rotation: settings.rotation,
      ...(settings.confirmRestrictedPaths === true ? { confirmRestrictedPaths: true } : {}),
      restart,
      expectedGeneration
    }, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  }

  async function saveGeodataSettings(input) {
    const urls = [input.geositeUrl, input.geositeSha256Url, input.geoipUrl, input.geoipSha256Url];
    const origins = typeof input.originsText === 'string' ? input.originsText.split(/\s+/).filter(Boolean) : [...new Set(urls.filter(Boolean).map(value => new URL(value).origin))];
    const payload = {
      expectedGeneration: input.expectedGeneration,
      settings: {
        ...(input.maxAssetSizeMB !== undefined ? { maxAssetSizeMB: Number(input.maxAssetSizeMB) } : {}),
        ...(input.userGeosite !== undefined ? { userGeosite: input.userGeosite } : {}),
        ...(input.userGeoip !== undefined ? { userGeoip: input.userGeoip } : {}),
        ...(input.enabled !== undefined ? { schedule: { enabled: input.enabled === true, time: input.time } } : {}),
        origins,
        geosite: { url: input.geositeUrl, checksum: { url: input.geositeSha256Url, algorithm: 'sha256' } },
        geoip: { url: input.geoipUrl, checksum: { url: input.geoipSha256Url, algorithm: 'sha256' } }
      }
    };
    return request('PUT', '/v1/xray/geodata/settings', payload);
  }

  async function updateGeodata() {
    return request('POST', '/v1/xray/geodata/update', undefined, {
      headers: { 'Idempotency-Key': `xrayui-${crypto.randomUUID()}` }
    });
  }

  const getTemplateStatus = () => helperClient.request("GET", "/v1/templates");
  const refreshTemplateStatus = () => request("POST", "/v1/templates/refresh", undefined, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  const getSystemSettings = () => helperClient.request("GET", "/v1/settings");
  const updateSystemSettings = (retainPerTemplate, connectivity) => request("PUT", "/v1/settings", { backup: { retainPerTemplate }, ...(connectivity ? { connectivity } : {}) });
  const getTemplateBackups = templateId => helperClient.request("GET", "/v1/templates/" + encodeURIComponent(templateId) + "/backups");
  const acceptSystemSettings = (templateId, expectedGeneration, expectedRevision) => request("POST", "/v1/templates/" + encodeURIComponent(templateId) + "/accept-system", { expectedGeneration, expectedRevision }, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  const applyTemplateSettings = (templateId, expectedGeneration, initialization) => request("POST", "/v1/templates/" + encodeURIComponent(templateId) + "/apply", { expectedGeneration, ...(initialization ? { initializeBlock: initialization.initializeBlock, expectedRevision: initialization.expectedRevision } : {}) }, { timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  const getSafeApply = () => helperClient.request("GET", "/v1/safe-apply");
  const resolveSafeApply = (transactionId, action) => request("POST", "/v1/safe-apply/" + encodeURIComponent(transactionId) + "/" + action, {});

  return { getTemplateBackups, acceptSystemSettings, applyTemplateSettings, getSafeApply, resolveSafeApply, getSystemSettings, updateSystemSettings, getTemplateStatus, refreshTemplateStatus, addProxyDomain, controlService, deleteProxyDomain, getProxyDomains, saveGeodataSettings, updateGeodata, updateLogSettings, validateConfiguration };
}

module.exports = { createHelperAdminService };
