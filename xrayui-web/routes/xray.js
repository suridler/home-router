const express = require('express');
const { createXrayController } = require('../controllers/xray');
const { asyncHandler } = require('../lib/async-handler');

function registerXrayRoutes(app, backend) {
  const router = express.Router();
  const controller = createXrayController(backend);
  router.get('/api/network/probe', asyncHandler(async (_req, res) => {
    res.json(await backend.helperClient.request('GET', '/v1/network/probe'));
  }));
  router.post('/api/network/probe', asyncHandler(async (req, res) => {
    res.json(await backend.helperClient.request('POST', '/v1/network/probe', {
      body: { stage: req.body?.stage, ...(req.body?.transactionId ? { transactionId: req.body.transactionId } : {}) }, timeoutMs: 45000
    }));
  }));
  router.post('/api/diagnostics/connectivity', asyncHandler(async (req, res) => {
    const result = await backend.helperClient.request('POST', '/v1/xray/diagnostics/connectivity', {
      body: { outboundTag: typeof req.body?.outboundTag === 'string' ? req.body.outboundTag : '' }, timeoutMs: 90000
    });
    res.json(result);
  }));
  router.post('/api/xray/config-test', asyncHandler(controller.testConfig));
  router.post('/api/xray/:action', asyncHandler(controller.action));
  router.post('/api/restart-xray', asyncHandler(controller.restart));
  router.post('/api/network/:action', asyncHandler(controller.networkAction));

  app.use(router);
}

module.exports = registerXrayRoutes;
