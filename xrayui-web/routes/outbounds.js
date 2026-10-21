const express = require('express');
const { createOutboundsController } = require('../controllers/outbounds');
const { asyncHandler } = require('../lib/async-handler');

function registerOutboundsRoutes(app, backend) {
  const router = express.Router();
  const controller = createOutboundsController(backend);
  router.post('/api/outbound-drafts/import', asyncHandler(controller.import));
  router.post('/api/outbound-drafts/apply', asyncHandler(controller.write('create')));
  router.get('/api/outbounds/:tag/references', asyncHandler(controller.references));
  router.get('/api/outbounds/:tag', asyncHandler(controller.edit));
  router.put('/api/outbounds/:tag', asyncHandler(controller.write('edit')));
  router.post('/api/outbounds/:tag/replace', asyncHandler(controller.write('replace')));
  router.delete('/api/outbounds/:tag', asyncHandler(controller.delete));
  app.use(router);
}

module.exports = registerOutboundsRoutes;
