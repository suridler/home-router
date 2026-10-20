'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { gatewayStatus } = require('../../services/gateway-status');

function facts() {
  return {
    readiness: { status: 'ready', checks: [{name: 'database', status: 'ready'}, {name: 'xray_api', status: 'ready'}] },
    helperHealth: { status: 'ok' },
    status: { configReadable: true, configParsed: true, configDrift: false },
    diagnostics: { checks: ['xray_service_active', 'tproxy_service_active', 'default_route'].map(name => ({name, status: 'ok'})) },
    geodata: { health: { state: 'disabled' } }, setup: { setupRequired: false }
  };
}

test('local readiness is separate from unmeasured internet and optional geodata updates', () => {
  const result = gatewayStatus(facts());
  assert.equal(result.state, 'ok');
  assert.equal(result.components.find(c => c.key === 'connectivity').state, 'unverified');
  assert.equal(result.components.find(c => c.key === 'geodata').state, 'disabled');
  assert.deepEqual(result.issues, []);
});

test('a stopped TProxy service is displayed as disabled, never a passing traffic check', () => {
  const input = facts();input.diagnostics.checks[1].status = 'mismatch';
  const result = gatewayStatus(input);
  assert.equal(result.state, 'ok');
  assert.equal(result.components.find(c => c.key === 'tproxy').state, 'disabled');
});

test('config drift, missing route and binding mismatch remain visible with a running Xray', () => {
  for (const breakFact of [
    input => {input.status.configDrift = true;},
    input => {input.diagnostics.checks[2].status = 'mismatch';},
    input => {input.diagnostics.checks.push({name:'declared_bindings', status:'mismatch'});}
  ]) {
    const input = facts();breakFact(input);const result = gatewayStatus(input);
    assert.equal(result.state,'error');assert.ok(result.issues.length);
    assert.equal(result.components.find(c=>c.key==='xray').state,'ok');
  }
});

test('missing observations require review rather than claiming stopped services or health', () => {
  const input = facts();input.diagnostics = {checks: []};input.helperHealth = null;input.setup = null;
  const result = gatewayStatus(input);
  assert.equal(result.state,'warning');
  assert.equal(result.components.find(c=>c.key==='xray').state,'unknown');
  assert.equal(result.components.find(c=>c.key==='system').state,'unknown');
  assert.ok(result.issues.some(c=>c.key==='setup'));
});

test('readiness and setup recovery failures always have a visible reason', () => {
  for (const name of ['database','recovery','templates','xray_api']) {
    const input = facts();input.readiness = {status:'degraded', checks:[{name,status:'degraded',code:name+'_failed'}]};
    const result = gatewayStatus(input);assert.equal(result.state,'error');assert.ok(result.issues.some(issue=>issue.key==='ready-'+name));
  }
  const input = facts();input.setup.recoveryRequired = true;
  assert.ok(gatewayStatus(input).issues.some(issue=>issue.key==='setup'));
});
