'use strict';
const assert=require('node:assert/strict');const{test}=require('node:test');const fs=require('node:fs');const path=require('node:path');const{createHelperAdminService}=require('../../services/helper-admin');
test('safe apply facade uses fixed status confirm and rollback endpoints',async()=>{const calls=[];const service=createHelperAdminService({helperClient:{request:async(method,requestPath,options={})=>{calls.push({method,requestPath,options});if(method==='GET')return{pending:{transactionId:'tx-1'}};return{changed:true}}}});assert.equal((await service.getSafeApply()).pending.transactionId,'tx-1');await service.resolveSafeApply('tx-1','confirm');await service.resolveSafeApply('tx-1','rollback');assert.deepEqual(calls.map(call=>[call.method,call.requestPath]),[['GET','/v1/safe-apply'],['POST','/v1/safe-apply/tx-1/confirm'],['POST','/v1/safe-apply/tx-1/rollback']])});
test('pending UI has countdown and explicit controls without auto-confirm',()=>{const app=fs.readFileSync(path.join(__dirname,'..','..','public','app.js'),'utf8');const html=fs.readFileSync(path.join(__dirname,'..','..','public','index.html'),'utf8');assert.match(app,/safeApplySeconds\(\)/);assert.match(app,/loadSafeApply\(\)/);assert.match(html,/class="safe-apply-banner"/);assert.match(html,/Настройки применены временно/);assert.match(html,/@click="resolveSafeApply\('confirm'\)"/);assert.match(html,/@click="resolveSafeApply\('rollback'\)"/);assert.doesNotMatch(app,/loadSafeApply.*resolveSafeApply\("confirm"\)/s)});

function appFixture() {
  const vm = require('node:vm');
  let factory;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../../public/app.js'), 'utf8'), {
    window: { confirm: () => true },
    document: { addEventListener: (_name, callback) => callback() },
    Alpine: { data: (_name, create) => { factory = create; } },
  });
  const app = factory(); app.safeApplyKnown = true; return app;
}

test('rollback preserves a durable reboot warning and refuses further confirmation', async () => {
  const app = appFixture();
  app.safeApply = { transactionId: 'tx-1', templateId: 'nftables' };
  let calls = 0;
  const waiting = { transactionId: 'tx-1', templateId: 'nftables', phase: 'awaiting_reboot', rebootRequired: true };
  app.api = async () => { calls++; return { transaction: waiting }; };
  app.loadTemplates = async () => {};
  await app.resolveSafeApply('rollback');
  assert.equal(app.safeApply.rebootRequired, true);
  assert.match(app.templateMessage, /REBOOT REQUIRED/);
  assert.match(app.templateMessage, /NOT restored/);
  await app.resolveSafeApply('confirm');
  assert.equal(calls, 1);
  app.api = async () => ({ pending: waiting });
  await app.loadSafeApply();
  assert.equal(app.safeApply.rebootRequired, true);
});

test('reboot alert is global prominent and not dismissible', () => {
  const html = fs.readFileSync(path.join(__dirname, '../../public/index.html'), 'utf8');
  const alert = html.match(/<section class="alert-banner reboot-required"[\s\S]*?<\/section>/)?.[0];
  assert.ok(alert);
  assert.match(alert, /role="alert"/);
  assert.match(alert, /safeApply\?\.rebootRequired/);
  assert.match(alert, /ТРЕБУЕТСЯ ПЕРЕЗАГРУЗКА/);
  assert.match(alert, /Автоматической перезагрузки не будет/);
  assert.doesNotMatch(alert, /<button|Dismiss|activeView/);
  assert.match(html, /!safeApply.rebootRequired/);
});

test('incomplete recovery permits retry but never confirmation', async () => {
  const app = appFixture();
  app.safeApply = { transactionId: 'group', templateId: 'sysctl', recoveryRequired: true };
  let calls = 0;
  app.api = async () => { calls++; return { transaction: { ...app.safeApply, recoveryRequired: false, rebootRequired: true } }; };
  app.loadTemplates = async () => {};
  await app.resolveSafeApply('confirm');
  assert.equal(calls, 0);
  await app.resolveSafeApply('rollback');
  assert.equal(calls, 1);
  assert.equal(app.safeApply.rebootRequired, true);
  assert.match(app.templateMessage, /nftables, sysctl and systemd/);
});


test('template apply and accept use completed generation after rollback', async () => {
  for (const action of ['applyTemplateSettings', 'acceptSystemSettings']) {
    const app = appFixture();
    app.status = { controlEnabled: true };
    app.loadTemplates = async () => {};
    const writes = [];
    app.api = async (_path, options) => {
      if (!options) return { generation: 2, completedGeneration: 1, backups: [] };
      writes.push(JSON.parse(options.body));
      return { accepted: true, transactionId: 'tx', deadline: new Date().toISOString() };
    };
    await app[action]({ templateId: 'sysctl', state: 'different', revision: 'r1' });
    assert.equal(writes.length, 1, app.templateMessage);
    assert.equal(writes[0].expectedGeneration, 1);
  }
});
