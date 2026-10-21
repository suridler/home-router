'use strict';
const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createGeodataController } = require('../../controllers/geodata');

test('web returns accepted operation without waiting for a status read', async () => {
  const operation = { id: 'accepted-operation', state: 'running' };
  const controller = createGeodataController({
    controlEnabled: true,
    updateGeodata: async () => ({ operation }),
    getGeodataStatus: async () => { throw new Error('admission must not wait for status'); }
  });
  const response = { code: 200, body: null,
    status(code) { this.code = code; return this; },
    json(body) { this.body = body; return this; }
  };
  await controller.update({}, response);
  assert.equal(response.code, 202);
  assert.deepEqual(response.body, { ok: true, operation });
});
