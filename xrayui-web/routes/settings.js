const express = require('express');
const { createSettingsController } = require('../controllers/settings');
const { asyncHandler } = require('../lib/async-handler');

function registerSettingsRoutes(app, backend) {
  const router = express.Router();
  const controller = createSettingsController(backend);
  router.get('/api/settings/system', asyncHandler(controller.getSystem));
  router.put('/api/settings/system', asyncHandler(controller.updateSystem));
  router.post('/api/settings/log-file', asyncHandler(controller.prepareLogFile));
  router.put('/api/settings/logs', asyncHandler(controller.updateLogs));

  app.use(router);
}

module.exports = registerSettingsRoutes;
