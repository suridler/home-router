'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createExporterTrafficReader } = require('../../services/exporter-traffic');

const T0 = Date.parse('2026-10-18T00:00:00.000Z');
const ENDPOINT = 'http://127.0.0.1:9101/api/v1/xray/stats';
const context = {
  inbounds: [{ tag: 'tproxy-in' }],
  outbounds: [{ tag: 'proxy', protocol: 'vless' }]
};

function counter(tag, direction, value, scope = 'outbound') {
  return {
    key: { Name: `${scope}>>>${tag}>>>traffic>>>${direction}`, Scope: scope, Tag: tag, Direction: direction },
    raw_bytes: value,
    cumulative_bytes: value
  };
}

function snapshot(at, counters, extra = {}) {
  return { collected_at: new Date(at).toISOString(), up: true, counters, ...extra };
}

function jsonResponse(payload, init = {}) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'content-type': 'application/json' },
    ...init
  });
}

function sequenceReader(samples, { now = () => T0, ...options } = {}) {
  const calls = [];
  const reader = createExporterTrafficReader({
    fetch: async (url, init) => {
      calls.push({ url, init });
      if (!samples.length) throw new Error('unexpected fetch');
      return jsonResponse(samples.shift());
    },
    now,
    ...options
  });
  return { reader, calls };
}

test('aggregates only allowlisted outbound counters with direction mapping and real deltas', async () => {
  const { reader, calls } = sequenceReader([
    snapshot(T0, [
      counter('proxy', 'uplink', 1000),
      counter('proxy', 'downlink', 5000)
    ]),
    snapshot(T0 + 2000, [
      counter('proxy', 'uplink', 1200),
      counter('proxy', 'downlink', 5400),
      counter('tproxy-in', 'uplink', 900000),
      counter('tproxy-in', 'downlink', 900000, 'inbound'),
      counter('unknown', 'uplink', 900000),
      counter('unknown', 'downlink', 900000)
    ])
  ], { now: () => T0 + 2500 });

  const first = await reader.read(context);
  assert.deepEqual(first.rates, null);
  assert.match(first.error, /second sample/i);
  assert.deepEqual(first.traffic, { in: '—', out: '—' });

  const second = await reader.read(context);
  assert.deepEqual(second.rates, { in: 200, out: 100 });
  assert.equal(second.error, null);
  assert.equal(second.source, 'xray-routing-exporter');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, ENDPOINT);
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.redirect, 'error');
});

test('reuses a repeated collected_at until stale, then resets the baseline', async () => {
  let clock = T0 + 2500;
  const repeated = snapshot(T0 + 2000, [
    counter('proxy', 'uplink', 1200),
    counter('proxy', 'downlink', 5400)
  ]);
  const { reader } = sequenceReader([
    snapshot(T0, [counter('proxy', 'uplink', 1000), counter('proxy', 'downlink', 5000)]),
    repeated,
    repeated,
    repeated
  ], { now: () => clock });

  await reader.read(context);
  assert.deepEqual((await reader.read(context)).rates, { in: 200, out: 100 });
  clock += 1000;
  assert.deepEqual((await reader.read(context)).rates, { in: 200, out: 100 });

  clock += 46000;
  const stale = await reader.read(context);
  assert.deepEqual(stale.rates, null);
  assert.match(stale.error, /stale/i);
  assert.deepEqual(stale.traffic, { in: '—', out: '—' });
});

test('rebaselines on counter resets, additions, removals and missing directions', async () => {
  const samples = [
    snapshot(T0, [counter('proxy', 'uplink', 100), counter('proxy', 'downlink', 100)]),
    snapshot(T0 + 1000, [counter('proxy', 'uplink', 150), counter('proxy', 'downlink', 200)]),
    snapshot(T0 + 2000, [counter('proxy', 'uplink', 10), counter('proxy', 'downlink', 20)]),
    snapshot(T0 + 3000, [counter('proxy', 'uplink', 20), counter('proxy', 'downlink', 40)]),
    snapshot(T0 + 4000, [
      counter('proxy', 'uplink', 20),
      counter('proxy', 'downlink', 40),
      counter('backup', 'uplink', 7),
      counter('backup', 'downlink', 9)
    ]),
    snapshot(T0 + 5000, [
      counter('proxy', 'uplink', 30),
      counter('proxy', 'downlink', 60),
      counter('backup', 'uplink', 17),
      counter('backup', 'downlink', 29)
    ]),
    snapshot(T0 + 6000, [counter('proxy', 'uplink', 40), counter('proxy', 'downlink', 80)]),
    snapshot(T0 + 7000, [counter('proxy', 'uplink', 50)]),
    snapshot(T0 + 8000, [counter('proxy', 'uplink', 60), counter('proxy', 'downlink', 100)]),
    snapshot(T0 + 9000, [counter('proxy', 'uplink', 70), counter('proxy', 'downlink', 120)])
  ];
  const { reader } = sequenceReader(samples, { now: () => T0 + 9500 });

  assert.match((await reader.read(context)).error, /second sample/i);
  assert.deepEqual((await reader.read(context)).rates, { in: 100, out: 50 });
  assert.match((await reader.read(context)).error, /reset/i);
  assert.deepEqual((await reader.read(context)).rates, { in: 20, out: 10 });

  const added = await reader.read({ ...context, outbounds: [...context.outbounds, { tag: 'backup' }] });
  assert.match(added.error, /counters changed/i);
  const afterAdd = await reader.read({ ...context, outbounds: [...context.outbounds, { tag: 'backup' }] });
  assert.deepEqual(afterAdd.rates, { in: 40, out: 20 });

  const removed = await reader.read(context);
  assert.match(removed.error, /counters changed/i);
  const missingDirection = await reader.read(context);
  assert.match(missingDirection.error, /missing a direction/i);
  assert.deepEqual(missingDirection.rates, null);

  const afterReset = await reader.read(context);
  assert.match(afterReset.error, /second sample/i);
  const recovered = await reader.read(context);
  assert.deepEqual(recovered.rates, { in: 20, out: 10 });
});

test('rejects backward timestamps and exporter URL changes', async () => {
  let clock = T0 + 2500;
  let url = 'http://127.0.0.1:9101';
  const samples = [
    snapshot(T0, [counter('proxy', 'uplink', 100), counter('proxy', 'downlink', 100)]),
    snapshot(T0 - 1000, [counter('proxy', 'uplink', 200), counter('proxy', 'downlink', 200)]),
    snapshot(T0 + 2000, [counter('proxy', 'uplink', 300), counter('proxy', 'downlink', 300)]),
    snapshot(T0 + 3000, [counter('proxy', 'uplink', 400), counter('proxy', 'downlink', 400)])
  ];
  let requested;
  const reader = createExporterTrafficReader({
    getURL: () => url,
    now: () => clock,
    fetch: async requestedURL => {
      requested = requestedURL;
      return jsonResponse(samples.shift());
    }
  });

  await reader.read(context);
  const backward = await reader.read(context);
  assert.match(backward.error, /backwards/i);
  assert.deepEqual(backward.rates, null);

  const afterRewind = await reader.read(context);
  assert.deepEqual(afterRewind.rates, { in: 100 / 3, out: 100 / 3 });
  clock += 3500;
  url = 'http://127.0.0.1:9200';
  const changed = await reader.read(context);
  assert.match(changed.error, /URL changed/i);
  assert.equal(requested, 'http://127.0.0.1:9200/api/v1/xray/stats');
});

test('returns stable failures for unavailable, invalid and unsafe snapshots', async () => {
  const cases = [
    { body: { collected_at: new Date(T0).toISOString(), up: false, counters: [] }, error: /unavailable/i },
    { body: { collected_at: 'not-a-time', up: true, counters: [] }, error: /timestamp/i },
    { body: { collected_at: new Date(T0 + 1000).toISOString(), up: true, counters: [] }, error: /missing a direction/i },
    { body: snapshot(T0, [{ key: { Scope: 'outbound', Tag: 'proxy', Direction: 'uplink' }, cumulative_bytes: Number.MAX_SAFE_INTEGER + 1 }]), error: /invalid/i },
    { body: snapshot(T0, [counter('proxy', 'uplink', '12'), counter('proxy', 'downlink', 1)]), error: /invalid/i },
    { body: { collected_at: new Date(T0 + 5000).toISOString(), up: true, counters: [counter('proxy', 'uplink', 1), counter('proxy', 'downlink', 1)] }, error: /future/i }
  ];

  for (const item of cases) {
    const reader = createExporterTrafficReader({
      fetch: async () => jsonResponse(item.body),
      now: () => T0 + 1000
    });
    const result = await reader.read(context);
    assert.deepEqual(result.rates, null);
    assert.deepEqual(result.traffic, { in: '—', out: '—' });
    assert.equal(result.source, 'xray-routing-exporter');
    assert.match(result.error, item.error);
  }

  const malformed = createExporterTrafficReader({
    fetch: async () => new Response('not json', { status: 200 }),
    now: () => T0
  });
  assert.match((await malformed.read(context)).error, /invalid JSON|non-JSON/i);
});

test('times out, reports network failures and never leaks the response body', async () => {
  const timeoutReader = createExporterTrafficReader({
    fetch: () => new Promise(() => {}),
    now: () => T0,
    timeoutMs: 20
  });
  // Real HTTP requests keep the event loop alive; this inert fetch mock does
  // not. Keep it alive until the reader's intentionally unreferenced timeout
  // fires, including on Node.js 22 used by release CI.
  const keepAlive = setInterval(() => {}, 1000);
  let timeout;
  try {
    timeout = await timeoutReader.read(context);
  } finally {
    clearInterval(keepAlive);
  }
  assert.match(timeout.error, /timed out/i);
  assert.deepEqual(timeout.rates, null);

  const networkReader = createExporterTrafficReader({
    fetch: async () => { throw new Error('connect ECONNREFUSED http://user:secret@127.0.0.1'); },
    now: () => T0
  });
  const network = await networkReader.read(context);
  assert.match(network.error, /unreachable/i);
  assert.doesNotMatch(network.error, /secret|ECONNREFUSED/);
});

test('rejects redirects, oversized bodies and untrusted exporter URLs', async () => {
  const redirectReader = createExporterTrafficReader({
    fetch: async () => new Response(null, { status: 302, headers: { location: 'http://example.com/' } }),
    now: () => T0
  });
  assert.match((await redirectReader.read(context)).error, /redirect/i);

  const oversizedReader = createExporterTrafficReader({
    fetch: async () => jsonResponse(snapshot(T0, [counter('proxy', 'uplink', 1), counter('proxy', 'downlink', 1)])),
    now: () => T0,
    maxBodyBytes: 20
  });
  assert.match((await oversizedReader.read(context)).error, /too large/i);

  let fetches = 0;
  for (const url of [
    'https://127.0.0.1:9101',
    'http://192.168.1.2:9101',
    'http://user:password@127.0.0.1:9101',
    'http://127.0.0.1:9101/?token=secret',
    'http://127.0.0.1:9101/other'
  ]) {
    const reader = createExporterTrafficReader({
      getURL: () => url,
      fetch: async () => { fetches += 1; return jsonResponse(snapshot(T0, [])); },
      now: () => T0
    });
    const result = await reader.read(context);
    assert.deepEqual(result.rates, null);
    assert.match(result.error, /trusted loopback/i);
  }
  assert.equal(fetches, 0);
});

test('coalesces overlapping reads into one bounded exporter request', async () => {
  let calls = 0;
  let resolveFetch;
  const reader = createExporterTrafficReader({
    fetch: () => {
      calls += 1;
      return new Promise(resolve => { resolveFetch = resolve; });
    },
    now: () => T0
  });

  const first = reader.read(context);
  const second = reader.read(context);
  assert.equal(calls, 1);
  resolveFetch(jsonResponse(snapshot(T0, [counter('proxy', 'uplink', 1), counter('proxy', 'downlink', 1)])));

  const [left, right] = await Promise.all([first, second]);
  assert.strictEqual(left, right);
  assert.match(left.error, /second sample/i);
  assert.equal(calls, 1);
});

test('uses metadata.outbounds tags as the outbound allowlist', async () => {
  const { reader } = sequenceReader([
    snapshot(T0, [counter('proxy', 'uplink', 0), counter('proxy', 'downlink', 0)]),
    snapshot(T0 + 1000, [counter('proxy', 'uplink', 10), counter('proxy', 'downlink', 20)])
  ], { now: () => T0 + 1000 });

  const metadataContext = { inbounds: [{ tag: 'tproxy-in' }], metadata: { outbounds: [{ tag: 'proxy' }] } };
  await reader.read(metadataContext);
  assert.deepEqual((await reader.read(metadataContext)).rates, { in: 20, out: 10 });
});

test('validates repeated snapshots before reusing cached rates', async () => {
  const good = at => snapshot(at, [counter('proxy', 'uplink', 100 + (at-T0)), counter('proxy', 'downlink', 200 + (at-T0))]);
  const {reader} = sequenceReader([good(T0),good(T0+1000),snapshot(T0+1000,[])], {now:()=>T0+2000});
  await reader.read(context);
  assert.ok((await reader.read(context)).rates);
  assert.equal((await reader.read(context)).rates,null);
});

test('same timestamp must have immutable counter values and allowlist', async () => {
  const samples = [snapshot(T0,[counter('proxy','uplink',10),counter('proxy','downlink',10)]),snapshot(T0,[counter('proxy','uplink',20),counter('proxy','downlink',20)])];
  const {reader}=sequenceReader(samples);
  await reader.read(context);
  assert.match((await reader.read(context)).error,/without advancing timestamp/);
});

test('Xray restart rebaselines even if persistent cumulative bytes keep increasing', async () => {
  const {reader}=sequenceReader([
    snapshot(T0,[counter('proxy','uplink',1),counter('proxy','downlink',2)],{start_unix:1,restart_count:0}),
    snapshot(T0+1000,[counter('proxy','uplink',2),counter('proxy','downlink',4)],{start_unix:2,restart_count:1})
  ],{now:()=>T0+2000});
  await reader.read(context);
  assert.match((await reader.read(context)).error,/restarted/);
});

test('lowercase counter fields are not the exporter contract', async () => {
  const {reader}=sequenceReader([snapshot(T0,[{key:{scope:'outbound',tag:'proxy',direction:'uplink'},cumulative_bytes:12}])]);
  assert.equal((await reader.read(context)).rates,null);
});
