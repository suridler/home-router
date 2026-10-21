const config = require('../config');
const helperClient = require('./helper');
const { createHelperReadService } = require('./helper-read-service');
const { createHelperMutationService } = require('./helper-mutations');
const { createHelperAdminService } = require('./helper-admin');
const { createHelperUpdateService } = require('./helper-updates');
const { createExporterTrafficReader } = require('./exporter-traffic');

// Facade for routes; domain logic lives in focused service modules.
function createBackend({ client = helperClient, trafficReader = createExporterTrafficReader({ getURL: () => config.exporterURL }).read, controlEnabled = config.controlEnabled } = {}) {
  const helperReads = createHelperReadService({ helperClient: client, controlEnabled, trafficReader });
  const helperMutations = createHelperMutationService({ helperClient: client });
  const helperAdmin = createHelperAdminService({ helperClient: client });
  const helperUpdates = createHelperUpdateService({ helperClient: client });
  return {
    ...require('./router'),
    ...helperReads,
    ...helperMutations,
    ...helperAdmin,
    ...helperUpdates,
    helperClient: client,
    controlEnabled,
    bootstrapToken: config.bootstrapToken
  };
}

module.exports = Object.assign(createBackend(), { createBackend });
