'use strict';

const http = require('node:http');

const DEFAULT_TIMEOUT_MS = 5000;
const DEFAULT_MAX_BODY_BYTES = 1 << 20;
const LONG_OPERATION_TIMEOUT_MS = 3 * 60 * 1000;
const MAX_REQUEST_TIMEOUT_MS = 15 * 60 * 1000;

class HelperClientError extends Error {
  constructor(message, { code, statusCode = 502, helperStatusCode, cause } = {}) {
    super(message, { cause });
    this.name = 'HelperClientError';
    this.code = code || 'helper_unavailable';
    this.statusCode = statusCode;
    if (helperStatusCode !== undefined) this.helperStatusCode = helperStatusCode;
  }
}

function createHelperClient({
  socketPath,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  maxBodyBytes = DEFAULT_MAX_BODY_BYTES
} = {}) {
  if (typeof socketPath !== 'string' || !socketPath.startsWith('/')) {
    throw new TypeError('helper socketPath must be an absolute path');
  }
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) {
    throw new TypeError('helper timeoutMs must be a positive integer');
  }
  if (!Number.isSafeInteger(maxBodyBytes) || maxBodyBytes <= 0) {
    throw new TypeError('helper maxBodyBytes must be a positive integer');
  }

  async function request(method, path, options = {}) {
    if (!/^[A-Z]+$/.test(method)) throw new TypeError('helper method must be uppercase');
    if (typeof path !== 'string' || !path.startsWith('/v1/') || path.includes('#')) {
      throw new TypeError('helper path must be an absolute /v1/ path');
    }
    const acceptedStatuses = options.acceptStatuses || [];
    if (!Array.isArray(acceptedStatuses) || acceptedStatuses.some(status => !Number.isInteger(status) || status < 100 || status > 599)) {
      throw new TypeError('helper acceptStatuses must contain valid HTTP status codes');
    }
    const requestTimeoutMs = options.timeoutMs === undefined ? timeoutMs : options.timeoutMs;
    if (!Number.isSafeInteger(requestTimeoutMs) || requestTimeoutMs <= 0 || requestTimeoutMs > MAX_REQUEST_TIMEOUT_MS) {
      throw new TypeError('helper request timeout must be between 1 ms and 15 minutes');
    }

    const encodedBody = options.body === undefined ? undefined : Buffer.from(JSON.stringify(options.body));
    if (encodedBody && encodedBody.length > maxBodyBytes) {
      throw new HelperClientError('Helper request exceeds the size limit', {
        code: 'helper_request_too_large',
        statusCode: 413
      });
    }

    const headers = { Accept: 'application/json', ...options.headers };
    if (encodedBody) {
      headers['Content-Type'] = 'application/json';
      headers['Content-Length'] = encodedBody.length;
    }

    return new Promise((resolve, reject) => {
      let timedOut = false;
      let timer;
      const succeed = payload => {
        clearTimeout(timer);
        resolve(payload);
      };
      const fail = error => {
        clearTimeout(timer);
        reject(error);
      };
      const req = http.request({ socketPath, method, path, headers }, response => {
        const chunks = [];
        let size = 0;
        let responseFailed = false;
        response.on('data', chunk => {
          if (responseFailed) return;
          size += chunk.length;
          if (size > maxBodyBytes) {
            responseFailed = true;
            fail(new HelperClientError('Helper response exceeds the size limit', {
              code: 'helper_response_too_large'
            }));
            response.destroy();
            return;
          }
          chunks.push(chunk);
        });
        response.on('end', () => {
          if (responseFailed) return;
          let payload;
          try {
            payload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
          } catch (error) {
            fail(new HelperClientError('Helper returned invalid JSON', {
              code: 'helper_invalid_response',
              cause: error
            }));
            return;
          }

          if ((response.statusCode >= 200 && response.statusCode < 300) || acceptedStatuses.includes(response.statusCode)) {
            succeed(payload);
            return;
          }

          const detail = payload && payload.error;
          if (!detail || typeof detail.code !== 'string' || typeof detail.message !== 'string') {
            fail(new HelperClientError('Helper returned an invalid error response', {
              code: 'helper_invalid_response',
              helperStatusCode: response.statusCode
            }));
            return;
          }
          fail(new HelperClientError(detail.message, {
            code: detail.code,
            statusCode: response.statusCode,
            helperStatusCode: response.statusCode
          }));
        });
        response.on('error', error => fail(
          error instanceof HelperClientError
            ? error
            : new HelperClientError('Helper response failed', { code: 'helper_unavailable', cause: error })
        ));
      });

      timer = setTimeout(() => {
        timedOut = true;
        req.destroy(new Error('helper request timed out'));
      }, requestTimeoutMs);
      timer.unref();
      req.on('error', error => fail(new HelperClientError(
        timedOut ? 'Helper request timed out' : 'Helper is unavailable',
        { code: timedOut ? 'helper_timeout' : 'helper_unavailable', statusCode: 503, cause: error }
      )));
      if (encodedBody) req.write(encodedBody);
      req.end();
    });
  }

  return Object.freeze({ request });
}

module.exports = {
  DEFAULT_MAX_BODY_BYTES,
  DEFAULT_TIMEOUT_MS,
  LONG_OPERATION_TIMEOUT_MS,
  MAX_REQUEST_TIMEOUT_MS,
  HelperClientError,
  createHelperClient
};
