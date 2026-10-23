'use strict';

const { LONG_OPERATION_TIMEOUT_MS, MAX_REQUEST_TIMEOUT_MS } = require('./helper-client');

const UPDATE_PATH = '/v1/update';

// Typed client for the helper's fixed self-update lifecycle. The web layer
// never exposes a generic helper proxy: every method below maps to one
// documented endpoint and one strict request shape.
function createHelperUpdateService({ helperClient }) {
  if (!helperClient || typeof helperClient.request !== 'function') throw new TypeError('helperClient is required');

  const request = (method, path, body, timeoutMs = LONG_OPERATION_TIMEOUT_MS) => helperClient.request(method, path, {
    ...(body === undefined ? {} : { body }),
    timeoutMs
  });

  async function getUpdateStatus() {
    return helperClient.request('GET', UPDATE_PATH);
  }

  async function checkUpdate() {
    return request('POST', `${UPDATE_PATH}/check`, {});
  }

  async function prepareUpdate(version) {
    // Staging a full release bundle (and its web dependencies) can take longer
    // than an ordinary mutation, but it must still remain bounded.
    return request('POST', `${UPDATE_PATH}/prepare`, { version }, MAX_REQUEST_TIMEOUT_MS);
  }

  async function applyUpdate(operationId, actionId) {
    return request('POST', `${UPDATE_PATH}/apply`, { operationId, actionId });
  }

  async function retryUpdate(operationId, actionId) {
    return request('POST', `${UPDATE_PATH}/retry`, { operationId, actionId });
  }

  async function rollbackUpdate(operationId, actionId) {
    return request('POST', `${UPDATE_PATH}/rollback`, { operationId, actionId });
  }

  return {
    getCoreUpdateStatus: () => request('GET', '/v1/xray-upgrade'),
    checkCoreUpdate: () => request('POST', '/v1/xray-upgrade/check', {}),
    applyCoreUpdate: actionId => request('POST', '/v1/xray-upgrade/apply', { actionId }),
    retryCoreUpdate: (operationId, actionId) => request('POST', '/v1/xray-upgrade/retry', { operationId, actionId }),
    rollbackCoreUpdate: (operationId, actionId) => request('POST', '/v1/xray-upgrade/rollback', { operationId, actionId }),
    getUpdateStatus,
    checkUpdate,
    prepareUpdate,
    applyUpdate,
    retryUpdate,
    rollbackUpdate
  };
}

module.exports = {
  UPDATE_PATH,
  createHelperUpdateService
};
