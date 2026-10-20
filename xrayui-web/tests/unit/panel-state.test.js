'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
function fixture(confirm = () => true) {
  let factory;
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'),'utf8'),{
    window:{location:{hash:''},confirm},document:{addEventListener:(_,fn)=>fn()},Alpine:{data:(_,fn)=>{factory=fn;}}
  });
  const app=factory();app.status={controlEnabled:true,generation:7,logs:{}};app.safeApplyKnown=true;return app;
}
function deferred(){let resolve;const promise=new Promise(r=>{resolve=r;});return{promise,resolve};}

test('unknown safety state and stale snapshots block every normal mutation handler',async()=>{
  for (const blocked of [{safeApplyKnown:false},{safeApplyError:'unreachable'},{safeApplyLoading:true},{statusStale:true},{safeApply:{transactionId:'pending'}},{pendingAction:'busy'}]) {
    const app=fixture();Object.assign(app,blocked);app.api=()=>assert.fail('blocked handler sent a request');
    for (const [name,args] of [
      ['addProxyDomain',['example.com']],['deleteProxyDomain',['example.com']],['runAction',['restart']],['activateNetwork',[]],
      ['saveSystemSettings',[]],['saveLogs',[]],['saveGeodata',[]],['updateGeodata',[]],['importVless',[]],['addVless',[]],['deleteOutbound',[]],
      ['saveRoutingRule',[]],['removeRoutingRule',[{tag:'test'}]],['moveRoutingRule',[{tag:'test'},'up']],
      ['applyTemplateSettings',[{templateId:'sysctl',state:'different'}]],['acceptSystemSettings',[{templateId:'sysctl',state:'different'}]]
    ]) await app[name](...args);
  }
});

test('template refresh does not release the lock held by another operation',async()=>{
  const app=fixture(),gate=deferred();app.pendingAction='safe-apply-confirm';app.api=()=>gate.promise;
  const read=app.loadTemplates(true);assert.equal(app.pendingAction,'safe-apply-confirm');gate.resolve({templates:[]});await read;
  assert.equal(app.pendingAction,'safe-apply-confirm');assert.equal(app.templatesLoading,false);
});

test('a pending safety read cannot be overwritten by a concurrent apply or confirm',async()=>{
  const app=fixture(),gate=deferred();app.safeApply={transactionId:'existing',deadline:new Date(Date.now()+10000).toISOString()};let writes=0;
  app.api=async(path,opts)=>{if(opts){writes++;return{};}return gate.promise;};
  const read=app.loadSafeApply();await app.resolveSafeApply('confirm');await app.applyTemplateSettings({templateId:'sysctl',state:'different'});
  assert.equal(writes,0);gate.resolve({pending:app.safeApply});await read;assert.equal(app.safeApply.transactionId,'existing');
});

test('failed safety refresh preserves the pending operation and blocks new writes until rechecked',async()=>{
  const app=fixture();app.safeApply={transactionId:'pending'};app.api=async()=>{throw Error('offline');};await app.loadSafeApply();
  assert.equal(app.safeApply.transactionId,'pending');assert.equal(app.canMutate(),false);assert.equal(app.safeApplyError,'offline');
  app.api=async()=>({pending:null});await app.loadSafeApply();assert.equal(app.canMutate(),true);
});

test('failed status poll keeps the snapshot but marks it stale; a fresh poll restores it',async()=>{
  const app=fixture(),previous=app.status;app.api=async()=>{throw Error('offline');};await app.loadStatus();
  assert.equal(app.status,previous);assert.equal(app.statusStale,true);assert.match(app.dashboardTitle(),/не подтверждено/);assert.equal(app.canMutate(),false);
  app.api=async()=>({controlEnabled:true,logs:{},dashboard:{state:'ok',issues:[]}});await app.loadStatus();
  assert.equal(app.statusStale,false);assert.ok(app.statusUpdatedAt);assert.equal(app.canMutate(),true);
});

test('late log reply is not rendered under a different selected journal',async()=>{
  const app=fixture(),gate=deferred(),calls=[];app.selectedLogKind='error';app.api=async path=>{calls.push(path);return calls.length===1?gate.promise:{lines:['ACCESS']};};
  const read=app.loadLogs();app.selectedLogKind='access';await app.loadLogs();gate.resolve({lines:['ERROR']});await read;
  assert.equal(app.logs[0],'ACCESS');assert.equal(app.loading.logs,false);assert.equal(calls.length,2);
});

test('outbound changed during reference check cannot delete a different connection',async()=>{
  const app=fixture(),gate=deferred(),calls=[];app.outboundRemoval.tag='checked-safe';
  app.api=async(path,opts)=>{calls.push([path,opts?.method]);if(opts?.method==='DELETE')assert.fail('wrong selection deleted');return gate.promise;};
  const removal=app.deleteOutbound();app.outboundRemoval.tag='changed';gate.resolve({outboundTag:'checked-safe',protected:false,references:[]});await removal;
  assert.equal(calls.length,1);assert.match(app.outboundMessage,/Выбор подключения изменился/);
});

test('saving log level preserves rotation unless the operator explicitly changes it',async()=>{
  for (const [choice,expected] of [['keep',undefined],['enable',true],['disable',false]]) {
    const app=fixture();app.logDraft.rotationChange=choice;app.logDraft.access.mode='file';app.loadStatus=async()=>{};let body;
    app.api=async(_,opts)=>{body=JSON.parse(opts.body);return{};};await app.saveLogs();
    assert.equal(body.rotation?.enabled,expected);if(choice==='keep')assert.equal('rotation' in body,false);
  }
});

test('restricted log paths require confirmation and resend the same generation only after consent',async()=>{
  for (const consent of [true,false]) {
    let prompts=0;
    const app=fixture(()=>{prompts++;return consent;}), bodies=[];
    app.logDirty=true;app.loadStatus=async()=>{};
    app.api=async(_,opts)=>{
      bodies.push(JSON.parse(opts.body));
      if(bodies.length===1)throw Object.assign(Error('restricted'),{code:'log_path_confirmation_required'});
      return{};
    };
    await app.saveLogs();
    assert.equal(prompts,1);assert.equal(bodies.length,consent?2:1);
    assert.equal('confirmRestrictedPaths' in bodies[0],false);
    if(consent){assert.equal(bodies[1].confirmRestrictedPaths,true);assert.equal(bodies[1].expectedGeneration,7);assert.equal(app.logDirty,false);}
    else {assert.equal(app.logDirty,true);assert.match(app.settingsMessage,/отменено/);}
  }
});

test('saved logging with failed restart is shown as persisted and refreshes state',async()=>{
  const app=fixture();let reads=0;app.logDirty=true;
  app.loadStatus=async()=>{reads++;};
  app.api=async()=>{throw Object.assign(Error('restart failed'),{code:'logging_saved_restart_failed'});};
  await app.saveLogs();
  assert.equal(reads,1);assert.equal(app.logDirty,false);
  assert.match(app.settingsMessage,/сохранены/);assert.match(app.settingsMessage,/не запустился/);
  assert.equal(app.pendingAction,'');
});

test('all settings subviews have stable URLs across reload and history',()=>{
  const app=fixture();for(const tab of ['logs','system']){app.navigateSettings(tab);assert.equal(app.activeSettingsTab,tab);assert.equal(app.viewFromHash(),'settings');app.activeSettingsTab='outbounds';assert.equal(app.viewFromHash(),'settings');assert.equal(app.activeSettingsTab,tab);}
});
