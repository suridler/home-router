'use strict';

const fs = require('node:fs/promises');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');

async function startFakeHelper(handler) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'xrayui-helper-test-'));
  const socketPath = path.join(directory, 'helper.sock');
  const server = http.createServer(handler);
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(socketPath, resolve);
  });

  return {
    socketPath,
    async close() {
      server.closeAllConnections();
      await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
      await fs.rm(directory, { recursive: true, force: true });
    }
  };
}

function writeJSON(response, statusCode, payload) {
  response.writeHead(statusCode, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify(payload));
}

module.exports = { startFakeHelper, writeJSON };
