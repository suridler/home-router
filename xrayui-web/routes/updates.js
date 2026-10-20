'use strict';

const express = require('express');
const { asyncHandler } = require('../lib/async-handler');

const VERSION_PATTERN = /^(?:helper-)?v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;
const ID_PATTERN = /^[a-f0-9]{32}$/;

function invalid(res, message) {
  res.status(400).json({ ok: false, code: 'invalid_request', message });
}

function controlDisabled(res) {
  res.status(403).json({
    ok: false,
    code: 'control_disabled',
    message: 'Update control is disabled; enable [web].control_enabled in helper and restart helper'
  });
}

function requestObject(req, allowedFields) {
  const body = req.body === undefined || req.body === null ? {} : req.body;
  if (typeof body !== 'object' || Array.isArray(body)) {
    return { error: 'request body must be a JSON object' };
  }
  for (const key of Object.keys(body)) {
    if (!allowedFields.includes(key)) return { error: `unknown field: ${key}` };
  }
  return { value: body };
}

function exactId(value) {
  return typeof value === 'string' && ID_PATTERN.test(value);
}

function registerUpdateRoutes(app, backend) {
  const router = express.Router();
  const enabled = () => backend.controlEnabled === true;

  function mutation(invoke) {
    return asyncHandler(async (req, res) => {
      if (!enabled()) return controlDisabled(res);
      const parsed = requestObject(req, ['operationId', 'actionId']);
      if (parsed.error) return invalid(res, parsed.error);
      const { operationId, actionId } = parsed.value;
      if (!exactId(operationId) || !exactId(actionId)) {
        return invalid(res, 'operationId and actionId must be 32 lowercase hexadecimal characters');
      }
      res.json(await invoke(operationId, actionId));
    });
  }

  router.get('/api/helper-update', asyncHandler(async (_req, res) => {
    const status = await backend.getUpdateStatus();
    // The update panel must remain usable when /api/status is unavailable
    // during recovery, so expose the immutable policy captured at startup.
    res.json({ ...status, controlEnabled: enabled() });
  }));

  router.post('/api/helper-update/check', asyncHandler(async (req, res) => {
    if (!enabled()) return controlDisabled(res);
    const parsed = requestObject(req, []);
    if (parsed.error) return invalid(res, parsed.error);
    res.json(await backend.checkUpdate());
  }));

  router.post('/api/helper-update/prepare', asyncHandler(async (req, res) => {
    if (!enabled()) return controlDisabled(res);
    const parsed = requestObject(req, ['version']);
    if (parsed.error) return invalid(res, parsed.error);
    const { version } = parsed.value;
    if (typeof version !== 'string' || !VERSION_PATTERN.test(version)) {
      return invalid(res, 'version must be an exact release tag');
    }
    res.json(await backend.prepareUpdate(version));
  }));

  router.post('/api/helper-update/apply', mutation((operationId, actionId) => (
    backend.applyUpdate(operationId, actionId)
  )));
  router.post('/api/helper-update/retry', mutation((operationId, actionId) => (
    backend.retryUpdate(operationId, actionId)
  )));
  router.post('/api/helper-update/rollback', mutation((operationId, actionId) => (
    backend.rollbackUpdate(operationId, actionId)
  )));

  app.use(router);
}

module.exports = registerUpdateRoutes;
module.exports.VERSION_PATTERN = VERSION_PATTERN;
module.exports.ID_PATTERN = ID_PATTERN;
