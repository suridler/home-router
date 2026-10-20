const express = require('express');
const { createStatusController } = require('../controllers/status');
const { asyncHandler } = require('../lib/async-handler');

function registerStatusRoutes(app, backend) {
  const router = express.Router();
  const controller = createStatusController(backend);
  router.get('/api/health', asyncHandler(controller.health));
  router.get('/api/status', asyncHandler(controller.status));
  router.get('/api/config', asyncHandler(controller.config));
  router.get('/api/alerts', asyncHandler(controller.alerts));

  app.use(router);
}

module.exports = registerStatusRoutes;
