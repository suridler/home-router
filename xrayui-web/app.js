const express = require('express');
const path = require('path');
const backend = require('./services/backend');
const registerStatusRoutes = require('./routes/status');
const registerGeodataRoutes = require('./routes/geodata');
const registerLogsRoutes = require('./routes/logs');
const registerSettingsRoutes = require('./routes/settings');
const registerXrayRoutes = require('./routes/xray');
const registerOutboundsRoutes = require('./routes/outbounds');
const registerRoutingRoutes = require('./routes/routing');
const registerProxyDomainsRoutes = require('./routes/proxy-domains');
const registerTemplateRoutes = require('./routes/templates');
const registerUpdateRoutes = require('./routes/updates');
const registerSetupRoutes = require('./routes/setup');
const { errorHandler } = require('./middleware/error-handler');

function createApp(services = backend) {
  const app = express();
  app.use(express.json());
  app.use('/vendor/alpinejs', express.static(path.join(__dirname, 'node_modules', 'alpinejs', 'dist')));
  app.use(express.static(path.join(__dirname, 'public')));

  registerSetupRoutes(app, services);
  registerStatusRoutes(app, services);
  registerGeodataRoutes(app, services);
  registerLogsRoutes(app, services);
  registerSettingsRoutes(app, services);
  registerXrayRoutes(app, services);
  registerOutboundsRoutes(app, services);
  registerRoutingRoutes(app, services);
  registerProxyDomainsRoutes(app, services);
  registerTemplateRoutes(app, services);
  registerUpdateRoutes(app, services);
  app.use(errorHandler);

  app.get('*', (_, res) => {
    res.sendFile(path.join(__dirname, 'public', 'index.html'));
  });
  return app;
}

module.exports = { app: createApp(), createApp };
