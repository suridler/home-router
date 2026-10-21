'use strict';

const { LONG_OPERATION_TIMEOUT_MS } = require('./helper-client');


function createHelperMutationService({ helperClient }) {
  if (!helperClient || typeof helperClient.request !== 'function') throw new TypeError('helperClient is required');

  const mutate = (method, path, body) => helperClient.request(method, path, { body, timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  const result = (metadata, extra = {}) => ({ ...extra, ...metadata });

  async function deleteOutbound(tag, restart, expectedGeneration) {
    const metadata = await mutate('DELETE', `/v1/xray/outbounds/${encodeURIComponent(tag)}`, { restart, expectedGeneration });
    return result(metadata, { outboundTag: tag, restart });
  }

  async function getOutboundReferences(tag) {
    const response = await helperClient.request('GET', `/v1/xray/outbounds/${encodeURIComponent(tag)}/references`);
    return { ...response, outboundTag: response.tag, references: response.references.map(path => ({ path, label: path })) };
  }

  function ruleInput(input) {
    const conditions = {};
    for (const [key, value] of Object.entries(input?.conditions || {})) {
      const items = Array.isArray(value) ? value : String(value).split(/[\n,]+/);
      const normalized = items.map(item => String(item).trim()).filter(Boolean);
      if (normalized.length) conditions[key] = normalized;
    }
    return {
      ruleTag: input?.ruleTag || '',
      conditions,
      outboundTag: input?.outboundTag || '',
      balancerTag: input?.balancerTag || ''
    };
  }

  async function addRoutingRule(input, position, restart, expectedGeneration) {
    return mutate('POST', '/v1/xray/routing/rules', { rule: ruleInput(input), position, restart, expectedGeneration });
  }

  async function editRoutingRule(id, input, restart, expectedGeneration) {
    return mutate('PUT', `/v1/xray/routing/rules/${encodeURIComponent(id)}`, { rule: ruleInput(input), restart, expectedGeneration });
  }

  async function deleteRoutingRule(id, restart, expectedGeneration) {
    return mutate('DELETE', `/v1/xray/routing/rules/${encodeURIComponent(id)}`, { restart, expectedGeneration });
  }

  async function moveRoutingRule(id, direction, restart, expectedGeneration) {
    return mutate('POST', `/v1/xray/routing/rules/${encodeURIComponent(id)}/move`, { direction, restart, expectedGeneration });
  }

  async function cloneRoutingRule(position, ruleTag, restart, expectedGeneration) {
    return mutate('POST', '/v1/xray/routing/rules/clone', { position, ruleTag, restart, expectedGeneration });
  }

  async function importOutbound(uri) {
    return mutate('POST', '/v1/xray/outbound-drafts/import', { uri });
  }
  async function editOutbound(tag) {
    return helperClient.request('GET', `/v1/xray/outbounds/${encodeURIComponent(tag)}`);
  }
  async function writeOutbound(operation, tag, outbound, restart, expectedGeneration, confirmReplacement) {
    const path = operation === 'create' ? '/v1/xray/outbound-drafts/apply' : `/v1/xray/outbounds/${encodeURIComponent(tag)}${operation === 'replace' ? '/replace' : ''}`;
    return mutate(operation === 'edit' ? 'PUT' : 'POST', path, { outbound, restart, expectedGeneration, confirmReplacement: confirmReplacement === true });
  }

  return {
    importOutbound, editOutbound, writeOutbound,
    addRoutingRule,
    cloneRoutingRule,
    deleteOutbound,
    deleteRoutingRule,
    editRoutingRule,
    getOutboundReferences,
    moveRoutingRule,
  };
}

module.exports = { createHelperMutationService };
