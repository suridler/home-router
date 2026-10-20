'use strict';
const express = require('express');
const crypto = require('node:crypto');
const { asyncHandler } = require('../lib/async-handler');
function equalToken(actual, expected) {
  if (!actual || !expected) return false;
  const a = Buffer.from(actual), b = Buffer.from(expected);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
function requireToken(expected) {
  return (req, res, next) => {
    if (equalToken(req.get('x-bootstrap-token'), expected)) return next();
    res.status(401).json({ ok: false, code: 'bootstrap_token_invalid', message: 'Bootstrap token is invalid' });
  };
}
// Completion revokes authority in this running process, even if apply's response
// was lost. Public status never includes token material.
function createBootstrapSession(initialToken) {
  let token = initialToken, revoked = false;
  return {
    accepts(value) { return !revoked && equalToken(value, token); },
    observe(status) {
      if (status.setupRequired === false && status.recoveryRequired !== true && status.status !== 'applying') {
        revoked = true; token = '';
      }
    },
    revoke() { revoked = true; token = ''; },
    get revoked() { return revoked; }
  };
}
function registerSetupRoutes(app, backend) {
  const router = express.Router();
  const session = createBootstrapSession(backend.bootstrapToken);
  const request = (method, path, body) => backend.helperClient.request(method, path, {
    ...(body === undefined ? {} : { body }),
    ...(path === '/v1/setup/apply' || path === '/v1/setup/recover' ? { timeoutMs: 6 * 60 * 1000 } : {})
  });
  router.get('/api/setup', asyncHandler(async (_, res) => {
    const status = await request('GET', '/v1/setup'); session.observe(status); res.json(status);
  }));
  router.use('/api/setup', asyncHandler(async (req, res, next) => {
    if (!session.accepts(req.get('x-bootstrap-token'))) return res.status(401).json({ ok: false, code: 'bootstrap_token_invalid', message: 'Bootstrap token is invalid or revoked' });
    const status = await request('GET', '/v1/setup'); session.observe(status);
    if (session.revoked) return res.status(410).json({ ok: false, code: 'bootstrap_session_completed', message: 'Setup is complete; bootstrap session is revoked' });
    if (status.status === 'applying' && req.method === 'POST') return res.status(409).json({ ok: false, code: 'setup_busy', message: 'Setup is running; inspect shared status' });
    next();
  }));
  router.get('/api/setup/interfaces', asyncHandler(async (_, res) => res.json(await request('GET', '/v1/setup/interfaces'))));
  router.post('/api/setup/plan', asyncHandler(async (req, res) => res.json(await request('POST', '/v1/setup/plan', { interface: req.body?.interface || '', address: req.body?.address || '', ...(req.body?.replaceXray === true ? { replaceXray: true } : {}) }))));
  router.post('/api/setup/apply', asyncHandler(async (req, res) => {
    const result = await request('POST', '/v1/setup/apply', req.body);
    if (result.status === 'configured_direct') session.revoke();
    res.json(result);
  }));
  router.post('/api/setup/adopt', asyncHandler(async (req, res) => {
    const result = await request('POST', '/v1/setup/adopt', { configFingerprint: req.body?.configFingerprint || '' });
    if (result.status === 'configured_direct') session.revoke();
    res.json(result);
  }));
  router.post('/api/setup/recover', asyncHandler(async (_, res) => res.json(await request('POST', '/v1/setup/recover'))));
  app.use(router);
}
module.exports = registerSetupRoutes;
module.exports.equalToken = equalToken;
module.exports.requireToken = requireToken;
module.exports.createBootstrapSession = createBootstrapSession;
