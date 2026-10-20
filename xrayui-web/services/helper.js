'use strict';

const config = require('../config');
const { createHelperClient } = require('./helper-client');

module.exports = createHelperClient({ socketPath: config.helperSocket });
