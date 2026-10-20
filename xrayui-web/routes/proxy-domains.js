'use strict';

const express = require('express');
const { createProxyDomainsController } = require('../controllers/proxy-domains');
const { asyncHandler } = require('../lib/async-handler');

function registerProxyDomainsRoutes(app, backend) {
  const router = express.Router();
  const controller = createProxyDomainsController(backend);
  router.get('/api/proxy-domains', asyncHandler(controller.list));
  router.post('/api/proxy-domains', asyncHandler(controller.add));
  router.delete('/api/proxy-domains/:domain', asyncHandler(controller.remove));
  app.use(router);
}

module.exports = registerProxyDomainsRoutes;
