'use strict';

const DEFAULT_EXPORTER_URL = 'http://127.0.0.1:9101';
const STATS_PATH = '/api/v1/xray/stats';
const SOURCE = 'xray-routing-exporter';
const DEFAULT_TIMEOUT_MS = 2000;
const DEFAULT_STALE_MS = 45000;
const DEFAULT_MAX_BODY_BYTES = 256 * 1024;
const { isIP } = require('node:net');

function trafficFailure(error) {
  return {
    traffic: { in: '—', out: '—' },
    rates: null,
    source: SOURCE,
    error
  };
}

function formatRate(value) {
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let unit = 0;
  let amount = value || 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount >= 100 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
}

function trafficSuccess(rates) {
  return {
    traffic: { in: formatRate(rates.in), out: formatRate(rates.out) },
    rates: { in: rates.in, out: rates.out },
    source: SOURCE,
    error: null
  };
}

function endpointFromBase(raw) {
  if (typeof raw !== 'string' || raw.trim() === '') throw new Error('url');
  const base = new URL(raw);
  if (base.protocol !== 'http:') throw new Error('url');
  if (base.username || base.password) throw new Error('url');
  if (base.search || base.hash) throw new Error('url');
  if (base.pathname !== '/' && base.pathname !== '') throw new Error('url');
  const host = base.hostname.replace(/^\[|\]$/g, '');
  if (!(host === '::1' || isIP(host) === 4 && host.startsWith('127.'))) throw new Error('url');
  return `${base.origin}${STATS_PATH}`;
}

function knownOutboundTags(context) {
  const lists = [];
  if (Array.isArray(context)) {
    lists.push(context);
  } else if (context && typeof context === 'object') {
    if (Array.isArray(context.outbounds)) lists.push(context.outbounds);
    if (context.metadata && Array.isArray(context.metadata.outbounds)) lists.push(context.metadata.outbounds);
  }
  const tags = new Set();
  for (const list of lists) {
    for (const item of list) {
      if (item && typeof item === 'object' && typeof item.tag === 'string' && item.tag.length > 0) {
        tags.add(item.tag);
      }
    }
  }
  return tags;
}

function snapshotCounters(snapshot, tags) {
  if (!Array.isArray(snapshot.counters)) throw new Error('counters');
  const counters = new Map();
  const directions = new Map();
  let uplinkTotal = 0;
  let downlinkTotal = 0;

  for (const counter of snapshot.counters) {
    const key = counter && typeof counter === 'object' ? counter.key : null;
    if (!key || typeof key !== 'object') continue;
    if (key.Scope !== 'outbound' || !tags.has(key.Tag)) continue;
    if (key.Direction !== 'uplink' && key.Direction !== 'downlink') throw new Error('counters');

    const value = counter.cumulative_bytes;
    if (!Number.isSafeInteger(value) || value < 0) throw new Error('counters');
    const id = `${key.Tag}\u0000${key.Direction}`;
    if (counters.has(id)) throw new Error('counters');
    counters.set(id, { direction: key.Direction, value });

    const seen = directions.get(key.Tag) || { uplink: false, downlink: false };
    seen[key.Direction] = true;
    directions.set(key.Tag, seen);

    if (key.Direction === 'uplink') uplinkTotal += value;
    else downlinkTotal += value;
    if (!Number.isSafeInteger(uplinkTotal) || !Number.isSafeInteger(downlinkTotal)) throw new Error('counters');
  }

  if (directions.size === 0 || directions.size !== tags.size) throw new Error('directions');
  for (const seen of directions.values()) {
    if (!seen.uplink || !seen.downlink) throw new Error('directions');
  }
  return { counters, uplinkTotal, downlinkTotal };
}

function sameCounterSet(left, right) {
  if (left.size !== right.size) return false;
  for (const key of left.keys()) if (!right.has(key)) return false;
  return true;
}

function failedResponse(response) {
  if (!response || response.redirected === true || response.ok === false) return true;
  return Number.isInteger(response.status) && (response.status < 200 || response.status >= 300);
}

async function readBoundedBody(response, maxBodyBytes) {
  const contentLength = response.headers && typeof response.headers.get === 'function'
    ? Number(response.headers.get('content-length'))
    : NaN;
  if (Number.isFinite(contentLength) && contentLength > maxBodyBytes) throw new Error('oversized');

  if (response.body && typeof response.body.getReader === 'function') {
    const reader = response.body.getReader();
    const chunks = [];
    let total = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        const chunk = Buffer.from(value);
        total += chunk.length;
        if (total > maxBodyBytes) {
          if (typeof reader.cancel === 'function') await reader.cancel().catch(() => {});
          throw new Error('oversized');
        }
        chunks.push(chunk);
      }
    } finally {
      if (typeof reader.releaseLock === 'function') reader.releaseLock();
    }
    return Buffer.concat(chunks).toString('utf8');
  }

  if (response.body && typeof response.body[Symbol.asyncIterator] === 'function') {
    const chunks = [];
    let total = 0;
    for await (const value of response.body) {
      const chunk = Buffer.from(value);
      total += chunk.length;
      if (total > maxBodyBytes) throw new Error('oversized');
      chunks.push(chunk);
    }
    return Buffer.concat(chunks).toString('utf8');
  }

  if (typeof response.text === 'function') {
    const text = await response.text();
    if (Buffer.byteLength(text, 'utf8') > maxBodyBytes) throw new Error('oversized');
    return text;
  }

  throw new Error('body');
}

function createExporterTrafficReader({
  getURL = () => DEFAULT_EXPORTER_URL,
  fetch: fetchImpl = globalThis.fetch,
  now = Date.now,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  staleMs = DEFAULT_STALE_MS,
  maxBodyBytes = DEFAULT_MAX_BODY_BYTES
} = {}) {
  if (typeof getURL !== 'function') throw new TypeError('getURL must be a function');
  if (typeof fetchImpl !== 'function') throw new TypeError('fetch must be a function');
  if (typeof now !== 'function') throw new TypeError('now must be a function');

  const timeout = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : DEFAULT_TIMEOUT_MS;
  const staleLimit = Number.isFinite(staleMs) && staleMs >= 0 ? staleMs : DEFAULT_STALE_MS;
  const bodyLimit = Number.isSafeInteger(maxBodyBytes) && maxBodyBytes > 0 ? maxBodyBytes : DEFAULT_MAX_BODY_BYTES;

  let baseline = null;
  let lastSuccess = null;
  let inFlight = null;

  function resetBaseline() {
    baseline = null;
    lastSuccess = null;
  }

  function fail(error, { keepBaseline = false } = {}) {
    if (!keepBaseline) resetBaseline();
    return trafficFailure(error);
  }

  async function requestSnapshot(url) {
    if (typeof AbortController !== 'function') {
      const response = await fetchImpl(url, { method: 'GET', redirect: 'error', headers: { accept: 'application/json' } });
      return { response, body: failedResponse(response) ? null : await readBoundedBody(response, bodyLimit) };
    }
    const controller = new AbortController();
    let timedOut = false;
    let timer;
    const timeoutPromise = new Promise((resolve, reject) => {
      timer = setTimeout(() => {
        timedOut = true;
        controller.abort();
        reject(new Error('timeout'));
      }, timeout);
      if (typeof timer.unref === 'function') timer.unref();
    });

    try {
      const response = await Promise.race([
        Promise.resolve(fetchImpl(url, {
          method: 'GET',
          redirect: 'error',
          headers: { accept: 'application/json' },
          signal: controller.signal
        })),
        timeoutPromise
      ]);
      const body = failedResponse(response)
        ? null
        : await Promise.race([readBoundedBody(response, bodyLimit), timeoutPromise]);
      return { response, body };
    } catch (error) {
      if (timedOut) throw new Error('timeout');
      throw error;
    } finally {
      clearTimeout(timer);
      controller.abort();
    }
  }

  async function run(context) {
    let url;
    try {
      url = endpointFromBase(getURL());
    } catch {
      return fail('Traffic exporter URL is not a trusted loopback HTTP endpoint');
    }

    let response;
    let body;
    try {
      const result = await requestSnapshot(url);
      response = result.response;
      body = result.body;
    } catch (error) {
      if (error && error.message === 'timeout') return fail('Traffic exporter request timed out');
      if (error && error.message === 'oversized') return fail('Traffic exporter response is too large');
      if (error && error.message === 'body') return fail('Traffic exporter response body is unavailable');
      return fail('Traffic exporter is unreachable');
    }

    if (!response || (response.redirected === true) ||
        (Number.isInteger(response.status) && response.status >= 300 && response.status < 400)) {
      return fail('Traffic exporter redirects are not allowed');
    }
    if (response.ok === false || (Number.isInteger(response.status) && (response.status < 200 || response.status >= 300))) {
      const status = Number.isInteger(response.status) ? ` (HTTP ${response.status})` : '';
      return fail(`Traffic exporter request failed${status}`);
    }

    if (!(response.headers?.get('content-type') || '').toLowerCase().includes('application/json')) return fail('Traffic exporter returned non-JSON data');

    let snapshot;
    try {
      snapshot = typeof body === 'string' ? JSON.parse(body) : body;
    } catch {
      return fail('Traffic exporter returned invalid JSON');
    }
    if (!snapshot || typeof snapshot !== 'object' || Array.isArray(snapshot)) {
      return fail('Traffic exporter returned an invalid snapshot');
    }
    if (snapshot.up !== true) return fail('Traffic exporter reports statistics unavailable');
    if (typeof snapshot.collected_at !== 'string') return fail('Traffic exporter timestamp is invalid');
    const collectedAt = Date.parse(snapshot.collected_at);
    if (!Number.isFinite(collectedAt)) return fail('Traffic exporter timestamp is invalid');

    const currentTime = now();
    if (!Number.isFinite(currentTime)) return fail('Traffic exporter clock is unavailable');
    if (collectedAt > currentTime) return fail('Traffic exporter timestamp is in the future');
    if (currentTime - collectedAt > staleLimit) return fail('Traffic exporter statistics are stale');

    let current;
    try { current = snapshotCounters(snapshot, knownOutboundTags(context)); }
    catch (error) { return fail(error?.message === 'directions' ? 'Traffic exporter counters are missing a direction' : 'Traffic exporter counters are invalid'); }
    current.url = url;
    current.collectedAt = collectedAt;
    current.epoch = JSON.stringify([snapshot.start_unix, snapshot.restart_count]);
    let reason = '';
    if (!baseline) reason = 'Traffic rate requires a second sample';
    else if (baseline.url !== url) reason = 'Traffic exporter URL changed; traffic baseline was reset';
    else if (collectedAt < baseline.collectedAt) reason = 'Traffic exporter timestamp moved backwards; traffic baseline was reset';
    else if (current.epoch !== baseline.epoch) reason = 'Xray restarted; traffic baseline was reset';
    else if (!sameCounterSet(baseline.counters, current.counters)) reason = 'Traffic exporter counters changed; traffic baseline was reset';
    if (reason) {
      baseline = current;
      lastSuccess = null;
      return fail(reason, { keepBaseline: true });
    }
    if (collectedAt === baseline.collectedAt) {
      for (const [id, counter] of current.counters) {
        if (counter.value !== baseline.counters.get(id).value) return fail('Traffic exporter changed counters without advancing timestamp');
      }
      return lastSuccess ? trafficSuccess(lastSuccess.rates) : fail('Traffic exporter statistics have not advanced', { keepBaseline: true });
    }

    for (const [id, counter] of current.counters) {
      if (counter.value < baseline.counters.get(id).value) {
        baseline = current;
        lastSuccess = null;
        return fail('Traffic exporter counters were reset; traffic baseline was reset', { keepBaseline: true });
      }
    }

    const elapsedSeconds = (collectedAt - baseline.collectedAt) / 1000;
    if (!(elapsedSeconds > 0)) return fail('Traffic exporter timestamps did not advance');

    const rates = {
      in: (current.downlinkTotal - baseline.downlinkTotal) / elapsedSeconds,
      out: (current.uplinkTotal - baseline.uplinkTotal) / elapsedSeconds
    };
    if (!Number.isFinite(rates.in) || !Number.isFinite(rates.out) || rates.in < 0 || rates.out < 0) {
      return fail('Traffic exporter rate calculation failed');
    }

    baseline = current;
    lastSuccess = { url, collectedAt, rates };
    return trafficSuccess(rates);
  }

  function read(context) {
    if (inFlight) return inFlight;
    const pending = run(context).finally(() => {
      if (inFlight === pending) inFlight = null;
    });
    inFlight = pending;
    return pending;
  }

  return { read };
}

module.exports = { createExporterTrafficReader };
