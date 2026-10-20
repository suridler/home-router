require('dotenv').config();
const { isIP } = require('node:net');

// No managed-setting fallback to HOST/PORT/XRAYUI_CONTROL_ENABLED. The
// production entrypoint must load helper settings before creating the app.
let settings;
function applyWebSettings(value) {
  if (!value || value.enabled !== true || typeof value.host !== 'string' ||
      !isIP(value.host) || ['0.0.0.0', '::'].includes(value.host) ||
      !Number.isSafeInteger(value.port) || value.port < 1 || value.port > 65535 ||
      typeof value.controlEnabled !== 'boolean') {
    throw new Error('web_settings_invalid');
  }
  if (value.exporterURL !== undefined && typeof value.exporterURL !== 'string') throw new Error('web_settings_invalid');
  if (value.exporterURL) {
    let url;
    try { url = new URL(value.exporterURL); } catch (_) { throw new Error('web_settings_invalid'); }
    const host = url.hostname.replace(/^\[|\]$/g, '');
    if (url.protocol !== 'http:' || url.username || url.password || url.search || url.hash || url.pathname !== '/' || !(host === '::1' || /^127\.(?:\d{1,3}\.){2}\d{1,3}$/.test(host) && isIP(host) === 4)) throw new Error('web_settings_invalid');
  }
  settings = Object.freeze({ exporterURL: value.exporterURL || '', host: value.host, port: value.port, controlEnabled: value.controlEnabled });
}

module.exports = Object.freeze({
  get port() { return settings?.port; },
  get exporterURL() { return settings?.exporterURL; },
  get host() { return settings?.host; },
  get controlEnabled() { return settings?.controlEnabled ?? false; },
  applyWebSettings,
  helperSocket: process.env.XRAY_CORE_HELPER_SOCKET || '/run/xray-core-helper/helper.sock',
  bootstrapToken: process.env.XRAYUI_BOOTSTRAP_TOKEN || "",
});
