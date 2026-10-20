function getFirst(search, name, fallback = '') {
  return search.has(name) ? search.get(name) : fallback;
}

function parseVlessUri(value, transportKey = 'network') {
  let uri;
  try {
    uri = new URL(String(value || '').trim());
  } catch (_) {
    throw new Error('VLESS URI is invalid');
  }
  if (uri.protocol !== 'vless:') throw new Error('URI must use the vless:// scheme');

  const id = decodeURIComponent(uri.username || '');
  const address = uri.hostname;
  const port = Number(uri.port || 443);
  if (!id) throw new Error('VLESS URI must include a user ID');
  if (!address) throw new Error('VLESS URI must include a server address');
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('VLESS URI has an invalid port');

  const type = getFirst(uri.searchParams, 'type', 'tcp');
  const security = getFirst(uri.searchParams, 'security', 'none');
  const supportedParameters = new Set([
    'type', 'security', 'flow', 'encryption', 'pbk', 'fp', 'sni', 'sid', 'spx',
    'alpn', 'path', 'mode', 'host', 'xPaddingBytes', 'sc', 'serviceName'
  ]);
  const unsupportedParameters = [...new Set([...uri.searchParams.keys()]
    .filter(key => !supportedParameters.has(key)))];
  const transport = transportKey === 'method'
    ? ({ tcp: 'raw', ws: 'websocket' }[type] || type)
    : type;
  const streamSettings = { [transportKey]: transport, security };

  if (type === 'xhttp') {
    const xhttpSettings = {};
    if (uri.searchParams.has('path')) xhttpSettings.path = getFirst(uri.searchParams, 'path');
    if (uri.searchParams.has('mode')) xhttpSettings.mode = getFirst(uri.searchParams, 'mode');
    if (uri.searchParams.has('host')) xhttpSettings.host = getFirst(uri.searchParams, 'host');
    const extra = {};
    if (uri.searchParams.has('xPaddingBytes')) extra.xPaddingBytes = getFirst(uri.searchParams, 'xPaddingBytes');
    if (uri.searchParams.has('sc')) extra.speedControl = getFirst(uri.searchParams, 'sc');
    if (Object.keys(extra).length) xhttpSettings.extra = extra;
    streamSettings.xhttpSettings = xhttpSettings;
  }

  if (security === 'reality') {
    streamSettings.realitySettings = {
      publicKey: getFirst(uri.searchParams, 'pbk'),
      fingerprint: getFirst(uri.searchParams, 'fp', 'chrome'),
      serverName: getFirst(uri.searchParams, 'sni'),
      shortId: getFirst(uri.searchParams, 'sid'),
      spiderX: getFirst(uri.searchParams, 'spx', '/')
    };
    if (uri.searchParams.has('alpn')) {
      streamSettings.realitySettings.alpn = getFirst(uri.searchParams, 'alpn').split(',').map(value => value.trim()).filter(Boolean);
    }
  } else if (security === 'tls') {
    streamSettings.tlsSettings = {
      serverName: getFirst(uri.searchParams, 'sni'),
      fingerprint: getFirst(uri.searchParams, 'fp', 'chrome')
    };
    if (uri.searchParams.has('alpn')) {
      streamSettings.tlsSettings.alpn = getFirst(uri.searchParams, 'alpn').split(',').map(value => value.trim()).filter(Boolean);
    }
  }

  if (type === 'grpc' && uri.searchParams.has('serviceName')) {
    streamSettings.grpcSettings = { serviceName: getFirst(uri.searchParams, 'serviceName') };
  } else if (type === 'ws') {
    streamSettings.wsSettings = {
      path: getFirst(uri.searchParams, 'path', '/'),
      headers: { Host: getFirst(uri.searchParams, 'sni') }
    };
  }

  return {
    settings: {
      vnext: [{
        address,
        port,
        users: [{ id, flow: getFirst(uri.searchParams, 'flow'), encryption: getFirst(uri.searchParams, 'encryption', 'none') }]
      }]
    },
    streamSettings,
    preview: {
      address,
      port,
      transport: type,
      security,
      flow: getFirst(uri.searchParams, 'flow') || 'not configured',
      serverName: getFirst(uri.searchParams, 'sni') || 'not configured',
      unsupportedParameters
    }
  };
}

module.exports = { parseVlessUri };
