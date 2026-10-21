'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const editor = require('../../public/outbound-editor');
const fixtures = require('../../../xray-core-helper/internal/outbound/testdata/manual.json');

function appFixture(confirm = () => true) {
  let factory;
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'), 'utf8'), {
    window: { OutboundEditor: editor, location: { hash: '' }, confirm },
    document: { addEventListener: (_, fn) => fn() }, Alpine: { data: (_, fn) => { factory = fn; } }
  });
  const app = factory(); app.status = { controlEnabled: true, generation: 7, outboundTags: [] }; app.safeApplyKnown = true;
  app.loadStatus = async () => {};
  return app;
}

test('all 13 native protocol forms preserve untouched unknown values', () => {
  assert.deepEqual(editor.protocols.slice().sort(), fixtures.map(item => item.protocol).sort());
  for (const fixture of fixtures) {
    const draft = structuredClone(fixture);
    draft.operatorExtension = { retained: true };
    for (const field of editor.fields(draft)) {
      const original = editor.get(draft, field.path);
      if (original !== undefined) editor.write(draft, field, editor.read(draft, field));
    }
    assert.deepEqual(draft, { ...fixture, operatorExtension: { retained: true } }, fixture.protocol);
  }
});

test('legacy server/user paths are edited in place, empty credentials clear instead of retaining secrets', () => {
  const draft = { protocol: 'vless', settings: { vnext: [{ address: 'edge.example', port: 443, users: [{ id: 'old-secret', encryption: 'none', custom: 42 }] }] }, streamSettings: { method: 'raw', sockopt: { mark: 255 } } };
  const fields = editor.fields(draft);
  const id = fields.find(field => field.path.endsWith('.id'));
  assert.equal(id.path, 'settings.vnext.0.users.0.id');
  editor.write(draft, id, '');
  assert.equal(draft.settings.vnext[0].users[0].id, undefined);
  assert.equal(draft.settings.vnext[0].users[0].custom, 42);
  assert.equal(draft.settings.address, undefined);
  assert.equal(fields.find(field => field.label === 'Транспорт').path, 'streamSettings.method');
  assert.equal(draft.streamSettings.sockopt.mark, 255);
});

test('import only creates a draft and is unavailable when editing an existing connection', async () => {
  const app = appFixture(); const calls = [];
  app.api = async (path, options) => { calls.push([path, JSON.parse(options.body)]); return { outbound: structuredClone(fixtures[2]), warnings: ['upstream warning'] }; };
  app.newConnection(); app.connection.uri = 'trojan://fixture-secret@edge.example:443';
  await app.importConnection();
  assert.equal(calls.length, 1); assert.equal(calls[0][0], '/api/outbound-drafts/import');
  assert.equal(app.connection.mode, 'create'); assert.equal(app.connection.draft.protocol, 'trojan'); assert.equal(app.connection.uri, '');
  app.connection.mode = 'edit'; await app.importConnection(); assert.equal(calls.length, 1);
});

test('ordinary edit uses the generation of the selected draft, not a later dashboard poll', async () => {
  const app = appFixture(); const calls = [];
  app.api = async (path, options) => {
    calls.push({ path, options });
    return options ? { generation: 9 } : { generation: 7, outbound: { tag: 'proxy', protocol: 'vless', settings: { id: 'actual-secret' } } };
  };
  await app.editConnection('proxy');
  assert.equal(app.connection.draft.settings.id, 'actual-secret');
  app.status.generation = 8;
  await app.saveConnection();
  const body = JSON.parse(calls[1].options.body);
  assert.equal(calls[1].options.method, 'PUT'); assert.equal(body.expectedGeneration, 7); assert.equal(body.confirmReplacement, false);
  assert.equal(app.connection.draft, null);
});

test('replacement starts from a new draft and sends explicit confirmation without old connection options', async () => {
  const app = appFixture(); const calls = [];
  app.api = async (path, options) => {
    calls.push({ path, options });
    return options ? { generation: 8 } : { generation: 7, outbound: { tag: 'proxy', protocol: 'vless', streamSettings: { sockopt: { mark: 255, dialerProxy: 'old' } }, mux: { enabled: true } } };
  };
  app.newConnection('trojan'); app.connection.draft.settings = { password: 'new-secret', address: 'edge.example', port: 443 }; app.connection.target = 'proxy';
  await app.prepareConnectionReplacement(); await app.saveConnection();
  const body = JSON.parse(calls[1].options.body);
  assert.equal(calls[1].path, '/api/outbounds/proxy/replace'); assert.equal(body.confirmReplacement, true); assert.equal(body.outbound.tag, 'proxy');
  assert.equal(body.outbound.mux, undefined); assert.equal(body.outbound.streamSettings, undefined);
});

test('declined replacement and blocked mutations never send a write', async () => {
  const app = appFixture(() => false); app.newConnection('freedom'); app.connection.mode = 'replace'; app.connection.target = 'proxy';
  app.api = () => assert.fail('unexpected request'); await app.saveConnection();
  app.connection.mode = 'create'; app.statusStale = true; await app.saveConnection();
});

test('editing a selected legacy server and user does not change neighbours', () => {
  const draft = { protocol: 'vmess', settings: { vnext: [
    { address: 'one.example', port: 443, users: [{ id: 'one' }] },
    { address: 'two.example', port: 443, users: [{ id: 'two' }, { id: 'three', custom: true }] }
  ] } };
  const field = editor.fields(draft, { server: 1, user: 1 }).find(item => item.path.endsWith('.id'));
  editor.write(draft, field, 'changed');
  assert.equal(draft.settings.vnext[1].users[1].id, 'changed');
  assert.equal(draft.settings.vnext[1].users[1].custom, true);
  assert.equal(draft.settings.vnext[1].users[0].id, 'two');
  assert.equal(draft.settings.vnext[0].users[0].id, 'one');
});

test('collections, headers and optional nested settings remain native values', () => {
  const draft = { protocol: 'freedom', settings: {} };
  editor.resize(draft, 'settings.finalRules', true);
  editor.write(draft, { path: 'settings.finalRules.0.action', type: 'text' }, 'block');
  editor.write(draft, { path: 'settings.finalRules.0.ip', type: 'list' }, '192.0.2.0/24\n198.51.100.1');
  assert.deepEqual(draft.settings.finalRules[0], { action: 'block', ip: ['192.0.2.0/24', '198.51.100.1'] });
  editor.resize(draft, 'settings.finalRules', false);
  assert.equal(draft.settings.finalRules.length, 0);
  editor.write(draft, { path: 'streamSettings.tlsSettings.serverName', type: 'text' }, 'edge.example');
  editor.write(draft, { path: 'streamSettings.tlsSettings.serverName', type: 'text' }, '');
  assert.equal(draft.streamSettings, undefined);
  editor.write(draft, { path: 'settings.headers', type: 'map' }, 'X-Example: https://example.com/path');
  assert.deepEqual(draft.settings.headers, { 'X-Example': 'https://example.com/path' });
  assert.throws(() => editor.write(draft, { path: 'settings.headers', type: 'map' }, 'malformed'), /имя: значение/);
});

test('a field error blocks saving until that field is corrected', async () => {
  const app = appFixture(); app.newConnection('http');
  const headers = { path: 'settings.headers', type: 'map' };
  app.changeConnection(headers, 'malformed');
  app.changeConnection({ path: 'settings.address', type: 'text' }, 'edge.example');
  app.api = () => assert.fail('invalid form was submitted');
  await app.saveConnection();
  assert.ok(app.connection.errors[headers.path]);
  app.changeConnection(headers, 'X-Test: value');
  assert.equal(Object.keys(app.connection.errors).length, 0);
});
