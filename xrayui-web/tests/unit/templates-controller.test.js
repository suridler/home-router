'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const { createTemplatesController } = require('../../controllers/templates');

function response() {
  return { statusCode: 200, payload: null, status(code) { this.statusCode = code; return this; }, json(payload) { this.payload = payload; return this; } };
}

test('template controller gates accept-system and forwards concurrency tokens', async () => {
  let call;
  const controller = createTemplatesController({
    controlEnabled: true,
    getTemplateStatus: async () => ({ templates: [] }),
    refreshTemplateStatus: async () => ({ templates: [] }),
    getTemplateBackups: async templateId => ({ templateId, generation: 3, backups: [] }),
    acceptSystemSettings: async (...args) => { call = args; return { accepted: true }; }
  });
  const res = response();
  await controller.acceptSystem({ params: { templateId: 'nftables' }, body: { expectedGeneration: 3, expectedRevision: 'revision' } }, res);
  assert.deepEqual(call, ['nftables', 3, 'revision']);
  assert.equal(res.payload.accepted, true);

  const disabled = createTemplatesController({ controlEnabled: false });
  const denied = response();
  await disabled.acceptSystem({ params: { templateId: 'nftables' }, body: {} }, denied);
  assert.equal(denied.statusCode, 503);
  assert.equal(denied.payload.code, 'control_disabled');
});

test('template UI confirms one named row and backup behavior without raw content', () => {
  const app = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'app.js'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');
  assert.match(app, /Принять системный файл «\$\{label\}»\?/);
  assert.match(app, /Текущий рабочий шаблон будет сохранён в резервной копии/);
  assert.match(html, /x-show="row\.state === 'different'"/);
  assert.match(html, /@click="acceptSystemSettings\(row\)"/);
  assert.doesNotMatch(html, /privateKey|raw file content/i);
});
