'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
function fixture() {
  let create;
  vm.runInNewContext(fs.readFileSync(require.resolve('../../public/app.js'),'utf8'),{
    document:{addEventListener:(_,fn)=>fn()},window:{},Alpine:{data:(_,fn)=>{create=fn;}}
  });return create();
}
test('durable error history distinguishes unavailable, disabled, pending, stale and fresh collection',()=>{
  const app=fixture();
  assert.match(app.errorHistoryStatus(),/недоступна/);
  app.status={errorHistory:{errors:[]}};
  assert.match(app.errorHistoryStatus(),/первый сбор/);
  app.status.errorHistory.source='none';
  assert.match(app.errorHistoryStatus(),/отключён/);
  app.status.errorHistory={collectionError:'log_collection_failed'};
  assert.match(app.errorHistoryStatus(),/ранее сохранённая/);
  app.status.errorHistory={collectedAt:'2000-01-01T00:00:00Z'};
  assert.match(app.errorHistoryStatus(),/давно не обновлялся/);
  app.status.errorHistory={collectedAt:new Date().toISOString(),truncated:true};
  assert.match(app.errorHistoryStatus(),/ограниченное окно/);
});

test('traffic availability explains warmup, reset, stale and unavailable exporter',()=>{
  const app=fixture();
  assert.match(app.trafficStatusLabel(),/Получаем/);
  for (const [error,expected] of [
    ['Traffic rate requires a second sample',/два свежих/],
    ['Xray restarted; traffic baseline was reset',/Счётчики изменились/],
    ['Traffic exporter statistics are stale',/устарели/],
    ['Traffic exporter is unreachable',/недоступен/],
    ['Traffic exporter URL is not a trusted loopback HTTP endpoint',/отключён/],
    ['Traffic exporter counters are invalid',/полные данные/]
  ]) { app.status={trafficError:error};assert.match(app.trafficStatusLabel(),expected); }
});
