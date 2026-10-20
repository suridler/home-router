const express = require('express');
const { createOutboundsController } = require('../controllers/outbounds');
const { asyncHandler } = require('../lib/async-handler');

function registerOutboundsRoutes(app, backend) {
  const router = express.Router();
  const controller = createOutboundsController(backend);
  router.post('/api/outbounds/vless/preview', asyncHandler(controller.previewVless));
  router.post('/api/outbounds/vless/new/preview', asyncHandler(controller.previewNewVless));
  router.post('/api/outbounds/vless', asyncHandler(controller.addVless));
  router.get('/api/outbounds/:tag/references', asyncHandler(controller.references));
  router.post('/api/outbounds/:tag/import-vless', asyncHandler(controller.importVless));
  router.delete('/api/outbounds/:tag', asyncHandler(controller.delete));
  app.use(router);
}

module.exports = registerOutboundsRoutes;
