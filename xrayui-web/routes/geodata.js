const express = require('express');
const { createGeodataController } = require('../controllers/geodata');
const { asyncHandler } = require('../lib/async-handler');

function registerGeodataRoutes(app, backend) {
  const router = express.Router();
  const controller = createGeodataController(backend);
  router.get('/api/settings/geodata', asyncHandler(controller.status));
  router.put('/api/settings/geodata', asyncHandler(controller.save));
  router.post('/api/geodata/update', asyncHandler(controller.update));

  app.use(router);
}

module.exports = registerGeodataRoutes;
