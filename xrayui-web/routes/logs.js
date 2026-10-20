const express = require('express');
const { createLogsController } = require('../controllers/logs');
const { asyncHandler } = require('../lib/async-handler');

function registerLogsRoutes(app, backend) {
  const router = express.Router();
  const controller = createLogsController(backend);
  router.get('/api/logs/:kind', asyncHandler(controller.list));

  app.use(router);
}

module.exports = registerLogsRoutes;
