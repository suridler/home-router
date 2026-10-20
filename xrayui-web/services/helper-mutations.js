'use strict';

const { LONG_OPERATION_TIMEOUT_MS } = require('./helper-client');

const protectedOutboundTags = new Set(['proxy', 'direct', 'block', 'dns-out']);

function createHelperMutationService({ helperClient }) {
  if (!helperClient || typeof helperClient.request !== 'function') throw new TypeError('helperClient is required');

  const mutate = (method, path, body) => helperClient.request(method, path, { body, timeoutMs: LONG_OPERATION_TIMEOUT_MS });
  const result = (metadata, extra = {}) => ({ ...extra, ...metadata });

  async function previewVlessImport(uri, outboundTag) {
    return { outboundTag, ...(await mutate('POST', '/v1/xray/outbounds/vless/preview', { uri })) };
  }

  async function previewNewVlessOutbound(uri, tag) {
    return { outboundTag: tag, ...(await mutate('POST', '/v1/xray/outbounds/vless/preview', { uri })) };
  }

  async function importVlessOutbound(uri, tag, restart, expectedGeneration) {
    const metadata = await mutate('PUT', `/v1/xray/outbounds/${encodeURIComponent(tag)}/vless`, { uri, restart, expectedGeneration });
    return result(metadata, { outboundTag: tag, restart });
  }

  async function addVlessOutbound(uri, tag, restart, expectedGeneration) {
    const metadata = await mutate('POST', '/v1/xray/outbounds/vless', { uri, tag, restart, expectedGeneration });
    return result(metadata, { outboundTag: tag, restart });
  }

  async function deleteOutbound(tag, restart, expectedGeneration) {
    const metadata = await mutate('DELETE', `/v1/xray/outbounds/${encodeURIComponent(tag)}`, { restart, expectedGeneration });
    return result(metadata, { outboundTag: tag, restart });
  }

  async function getOutboundReferences(tag) {
    const [outbounds, routing] = await Promise.all([
      helperClient.request('GET', '/v1/xray/outbounds'),
      helperClient.request('GET', '/v1/xray/routing')
    ]);
    if (!(outbounds.outbounds || []).some(outbound => outbound.tag === tag)) {
      const error = new Error(`Outbound '${tag}' was not found`);
      error.code = 'outbound_not_found';
      error.statusCode = 404;
      throw error;
    }
    const references = [];
    for (const rule of routing.rules || []) {
      if (rule.outboundTag === tag) references.push({ path: `routing.rules[${rule.index - 1}].outboundTag`, label: rule.tag || `rule ${rule.index}` });
    }
    for (const [index, balancer] of (routing.balancers || []).entries()) {
      if ((balancer.selector || []).includes(tag)) references.push({ path: `routing.balancers[${index}].selector`, label: balancer.tag || `balancer ${index + 1}` });
    }
    return { outboundTag: tag, protected: protectedOutboundTags.has(tag), references };
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

  return {
    addRoutingRule,
    addVlessOutbound,
    cloneRoutingRule,
    deleteOutbound,
    deleteRoutingRule,
    editRoutingRule,
    getOutboundReferences,
    importVlessOutbound,
    moveRoutingRule,
    previewNewVlessOutbound,
    previewVlessImport
  };
}

module.exports = { createHelperMutationService };
