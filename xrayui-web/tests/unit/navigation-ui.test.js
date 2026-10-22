'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
function fixture(hash = '') {
  let factory;
  const window = { location: { hash } };
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'), 'utf8'), {
    window, document: { addEventListener: (_, callback) => callback() },
    Alpine: { data: (_, create) => { factory = create; } }
  });
  return factory();
}
test('direct navigation and reloading select the correct settings section', () => {
  const app = fixture();
  for (const view of ['outbounds', 'geodata', 'templates', 'settings', 'logging', 'updates']) {
    app.navigationOpen = true;
    app.navigate(view);
    assert.equal(app.activeView, view);
    assert.equal(app.navigationOpen, false);
    assert.ok(app.viewTitle());
    assert.equal(fixture('#' + view).viewFromHash(), view);
  }
  assert.equal(fixture('#unknown').viewFromHash(), 'status');
  assert.equal(fixture('#settings/logs').viewFromHash(), 'logging');
  assert.equal(fixture('#settings/system').viewFromHash(), 'settings');
  assert.equal(fixture('#maintenance').viewFromHash(), 'network');
  assert.equal(fixture('#xray').viewFromHash(), 'network');
});
test('component cards open their actual administration section', () => {
  const app = fixture();
  for (const [component, view] of [['helper', 'settings'], ['system', 'network'], ['geodata', 'geodata'], ['xray', 'network']]) {
    app.openComponent(component);
    assert.equal(app.activeView, view);
  }
});
test('overview distinguishes missing proxy and disabled updates from a connectivity check', () => {
  const app = fixture();
  assert.match(app.dashboardTitle(), /Получаем/);
  app.status = { proxyState: 'direct-placeholder', dashboard: { state: 'ok', issues: [] } };
  assert.equal(app.proxyLabel(), 'Прямой выход');
  assert.match(app.dashboardDescription(), /автоматически ещё не проверялся/);
  assert.match(app.issueSummary({ title: 'Geodata', state: 'disabled' }), /Автообновление/);
});
