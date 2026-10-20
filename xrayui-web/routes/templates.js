'use strict';

const express = require('express');
const { createTemplatesController } = require('../controllers/templates');
const { asyncHandler } = require('../lib/async-handler');

function registerTemplateRoutes(app, backend) {
  const router = express.Router();
  const controller = createTemplatesController(backend);
  router.get('/api/templates', asyncHandler(controller.status));
  router.post('/api/templates/refresh', asyncHandler(controller.refresh));
  router.get('/api/templates/:templateId/backups', asyncHandler(controller.backups));
  router.post('/api/templates/:templateId/accept-system', asyncHandler(controller.acceptSystem));
  router.post('/api/templates/:templateId/apply', asyncHandler(controller.applyTemplate));
  router.get('/api/safe-apply', asyncHandler(controller.safeStatus));
  router.post('/api/safe-apply/:transactionId/confirm', asyncHandler(controller.confirmSafeApply));
  router.post('/api/safe-apply/:transactionId/rollback', asyncHandler(controller.rollbackSafeApply));
  app.use(router);
}

module.exports = registerTemplateRoutes;
