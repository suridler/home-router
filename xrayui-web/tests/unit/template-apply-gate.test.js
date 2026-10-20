'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const { createHelperAdminService } = require('../../services/helper-admin');
const { createTemplatesController } = require('../../controllers/templates');
const { LONG_OPERATION_TIMEOUT_MS } = require('../../services/helper-client');

function response() {
  return { statusCode: 200, payload: null, status(code) { this.statusCode = code; return this; }, json(payload) { this.payload = payload; return this; } };
}

test('web apply forwards only template ID and expected generation', async () => {
  let helperCall;
  const service = createHelperAdminService({ helperClient: { request: async (method, requestPath, options) => {
    helperCall = { method, requestPath, options };
    const error = new Error('confirmed safe apply is required');
    error.code = 'safe_apply_required'; error.statusCode = 409; throw error;
  } } });
  await assert.rejects(service.applyTemplateSettings('nftables', 7), error => error.code === 'safe_apply_required');
  assert.equal(helperCall.method, 'POST');
  assert.equal(helperCall.requestPath, '/v1/templates/nftables/apply');
  assert.deepEqual(helperCall.options.body, { expectedGeneration: 7 });
  assert.equal(helperCall.options.timeoutMs, LONG_OPERATION_TIMEOUT_MS);

  let controllerCall;
  const controller = createTemplatesController({ controlEnabled: true, applyTemplateSettings: async (...args) => {
    controllerCall = args; const error = new Error('confirmed safe apply is required'); error.code = 'safe_apply_required'; error.statusCode = 409; throw error;
  } });
  const result = response();
  await controller.applyTemplate({ params: { templateId: 'nftables' }, body: { expectedGeneration: 7 } }, result);
  assert.deepEqual(controllerCall, ['nftables', 7]);
  assert.equal(result.statusCode, 409);
  assert.equal(result.payload.code, 'safe_apply_required');
});

test('template apply UI exposes the safety gate without raw configuration', () => {
  const app = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');
  assert.match(app, /Изменения будут временными и потребуют подтверждения/);
  assert.match(html, /@click="applyTemplateSettings\(row\)"/);
  assert.match(html, /x-show="row\.state === 'different'"/);
  assert.doesNotMatch(app + html, /privateKey|raw file content/i);
});
