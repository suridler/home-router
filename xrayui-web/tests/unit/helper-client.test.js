'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { HelperClientError, createHelperClient } = require('../../services/helper-client');
const { startFakeHelper, writeJSON } = require('../helpers/fake-helper');

test('decodes a successful helper JSON response', async t => {
  const fake = await startFakeHelper((request, response) => {
    assert.equal(request.method, 'GET');
    assert.equal(request.url, '/v1/health');
    writeJSON(response, 200, { status: 'ok', version: 'test' });
  });
  t.after(() => fake.close());

  const client = createHelperClient({ socketPath: fake.socketPath });
  assert.deepEqual(await client.request('GET', '/v1/health'), { status: 'ok', version: 'test' });
});

test('fails a bounded request with a stable timeout error', async t => {
  const fake = await startFakeHelper(() => {});
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath, timeoutMs: 25 });

  await assert.rejects(client.request('GET', '/v1/ready'), error => {
    assert.ok(error instanceof HelperClientError);
    assert.equal(error.code, 'helper_timeout');
    assert.equal(error.statusCode, 503);
    return true;
  });
});

test('rejects malformed JSON without exposing the response body', async t => {
  const fake = await startFakeHelper((request, response) => {
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end('{"secret":"unterminated"');
  });
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath });

  await assert.rejects(client.request('GET', '/v1/health'), error => {
    assert.ok(error instanceof HelperClientError);
    assert.equal(error.code, 'helper_invalid_response');
    assert.doesNotMatch(error.message, /secret|unterminated/);
    return true;
  });
});

test('preserves typed helper error status, code and message', async t => {
  const fake = await startFakeHelper((request, response) => {
    writeJSON(response, 409, { error: { code: 'generation_conflict', message: 'configuration generation changed' } });
  });
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath });

  await assert.rejects(client.request('PUT', '/v1/xray/logging', { body: { expectedGeneration: 3 } }), error => {
    assert.ok(error instanceof HelperClientError);
    assert.equal(error.statusCode, 409);
    assert.equal(error.helperStatusCode, 409);
    assert.equal(error.code, 'generation_conflict');
    assert.equal(error.message, 'configuration generation changed');
    return true;
  });
});

test('bounds request and response bodies', async t => {
  const fake = await startFakeHelper((request, response) => {
    writeJSON(response, 200, { value: 'response larger than the configured test limit' });
  });
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath, maxBodyBytes: 32 });

  await assert.rejects(
    client.request('POST', '/v1/proxy-domains', { body: { domain: 'request-is-too-large.example' } }),
    { code: 'helper_request_too_large', statusCode: 413 }
  );
  await assert.rejects(client.request('GET', '/v1/health'), error => {
    assert.ok(error instanceof HelperClientError);
    assert.equal(error.code, 'helper_response_too_large');
    return true;
  });
});

test('can accept the helper readiness degraded status as a typed projection', async t => {
  const fake = await startFakeHelper((request, response) => {
    writeJSON(response, 503, { status: 'degraded', checks: [{ name: 'xray_api', status: 'degraded', code: 'xray_api_unavailable' }] });
  });
  t.after(() => fake.close());
  const client = createHelperClient({ socketPath: fake.socketPath });

  const readiness = await client.request('GET', '/v1/ready', { acceptStatuses: [503] });
  assert.equal(readiness.status, 'degraded');
  assert.equal(readiness.checks[0].code, 'xray_api_unavailable');
});
