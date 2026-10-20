'use strict';

const express = require('express');
const { createRoutingController } = require('../controllers/routing');
const { asyncHandler } = require('../lib/async-handler');

function registerRoutingRoutes(app, backend) {
  const router = express.Router();
  const controller = createRoutingController(backend);
  router.post('/api/routing/rules', asyncHandler(controller.add));
  router.post('/api/routing/rules/clone', asyncHandler(controller.clone));
  router.put('/api/routing/rules/:id', asyncHandler(controller.edit));
  router.delete('/api/routing/rules/:id', asyncHandler(controller.remove));
  router.post('/api/routing/rules/:id/move', asyncHandler(controller.move));
  app.use(router);
}

module.exports = registerRoutingRoutes;
