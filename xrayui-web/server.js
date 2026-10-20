const config = require('./config');

async function start({ client = require('./services/helper'), loadApp = () => require('./app') } = {}) {
  const settings = await client.request('GET', '/v1/web/settings');
  config.applyWebSettings(settings);
  // Backend captures the helper-owned control policy when the app is loaded.
  const { app } = loadApp();
  const server = await new Promise((resolve, reject) => {
    const listener = app.listen(config.port, config.host, () => resolve(listener));
    listener.once('error', reject);
  });
  return server;
}

if (require.main === module) {
  start().then(() => console.log(`xrayui-web listening on http://${config.host}:${config.port}`))
    .catch(() => { console.error('xrayui-web startup failed: helper settings or listener unavailable'); process.exitCode = 1; });
}

module.exports = { start };
