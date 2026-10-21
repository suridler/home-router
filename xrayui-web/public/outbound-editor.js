/* Native Xray JSON is retained in the draft. Descriptors describe form controls only. */
(function (root) {
  'use strict';
  const protocols = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http', 'wireguard', 'hysteria', 'masque', 'freedom', 'blackhole', 'dns', 'loopback'];
  const field = (path, label, type = 'text', options) => ({ path, label, type, options });
  const get = (object, path) => path.split('.').reduce((value, key) => value?.[key], object);
  function set(object, path, value) {
    const keys = path.split('.'); let target = object;
    for (let i = 0; i < keys.length - 1; i++) {
      const key = keys[i];
      if (!target[key] || typeof target[key] !== 'object') target[key] = /^\d+$/.test(keys[i + 1]) ? [] : {};
      target = target[key];
    }
    const key = keys.at(-1);
    if (value === undefined) {
      delete target[key];
      // Clearing the last optional scalar also removes its now-empty object.
      for (let i = keys.length - 1; i > 0; i--) {
        const parent = get(object, keys.slice(0, i - 1).join('.')) || object;
        const child = parent[keys[i - 1]];
        if (child && !Array.isArray(child) && typeof child === 'object' && Object.keys(child).length === 0) delete parent[keys[i - 1]];
        else break;
      }
    } else target[key] = value;
  }
  const settings = {
    vless: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['id', 'UUID / ID'], ['encryption', 'Encryption'], ['flow', 'Flow']],
    vmess: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['id', 'UUID'], ['security', 'Шифрование']],
    trojan: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['password', 'Пароль', 'password']],
    shadowsocks: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['method', 'Метод шифрования'], ['password', 'Пароль', 'password']],
    socks: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['user', 'Пользователь'], ['pass', 'Пароль', 'password']],
    http: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['user', 'Пользователь'], ['pass', 'Пароль', 'password']],
    wireguard: [['secretKey', 'Приватный ключ', 'password'], ['address', 'Адреса интерфейса', 'list'], ['mtu', 'MTU', 'number'], ['noKernelTun', 'Без kernel TUN', 'boolean'], ['remoteDNS', 'Удалённые DNS', 'list']],
    hysteria: [['version', 'Версия', 'number'], ['address', 'Сервер'], ['port', 'Порт', 'number']],
    masque: [['address', 'Сервер'], ['port', 'Порт', 'number'], ['remoteDNS', 'Удалённые DNS', 'list']],
    freedom: [['domainStrategy', 'Domain strategy'], ['redirect', 'Redirect'], ['userLevel', 'Уровень', 'number']],
    blackhole: [['response.type', 'Ответ', 'select', ['none', 'http']]],
    dns: [['network', 'Сеть', 'select', ['tcp', 'udp']], ['address', 'Сервер'], ['port', 'Порт', 'number'], ['userLevel', 'Уровень', 'number']],
    loopback: [['inboundTag', 'Тег входящего подключения']]
  };
  function settingPath(draft, key, selection = {}) {
    const s = draft.settings || {};
    const serverIndex = selection.server || 0, userIndex = selection.user || 0;
    if (s.vnext?.length) {
      if (['address', 'port'].includes(key)) return `settings.vnext.${serverIndex}.${key}`;
      return `settings.vnext.${serverIndex}.users.${userIndex}.${key}`;
    }
    if (s.servers?.length) {
      const server = s.servers[serverIndex];
      if (!['address', 'port', 'method', 'password'].includes(key) && server.users?.length) return `settings.servers.${serverIndex}.users.${userIndex}.${key}`;
      return `settings.servers.${serverIndex}.${key}`;
    }
    return `settings.${key}`;
  }
  function fields(draft, selection = {}) {
    const list = (settings[draft.protocol] || []).map(([key, label, type, options]) => field(settingPath(draft, key, selection), label, type, options));
    if (['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http'].includes(draft.protocol)) list.push(field(settingPath(draft, 'level', selection), 'Уровень', 'number'), field(settingPath(draft, 'email', selection), 'Email'));
    if (draft.protocol === 'vless') {
      list.push(field(settingPath(draft, 'seed', selection), 'Seed'), field(settingPath(draft, 'reverse.tag', selection), 'Reverse tag'));
      sniffingFields(list, settingPath(draft, 'reverse.sniffing', selection));
    }
    if (draft.protocol === 'trojan') list.push(field(settingPath(draft, 'flow', selection), 'Flow'));
    if (draft.protocol === 'http') list.push(field('settings.headers', 'HTTP headers (имя: значение)', 'map'));
    if (draft.protocol === 'wireguard') list.push(field('settings.reserved', typeof draft.settings?.reserved === 'string' ? 'Reserved (Base64)' : 'Reserved (байты через запятую)', typeof draft.settings?.reserved === 'string' ? 'text' : 'numbers'));
    if (draft.protocol === 'freedom') {
      for (const [key, label, type] of [['targetStrategy', 'Target strategy'], ['proxyProtocol', 'PROXY protocol', 'number'], ['ipsBlocked', 'Legacy ipsBlocked', 'list'], ['fragment.packets', 'Fragment packets'], ['fragment.length', 'Fragment length'], ['fragment.interval', 'Fragment interval'], ['fragment.maxSplit', 'Fragment maxSplit']]) list.push(field(`settings.${key}`, label, type));
      for (const path of ['noise', 'noises']) {
        const count = path === 'noise' ? (draft.settings?.noise ? 1 : 0) : (draft.settings?.noises || []).length;
        for (let i = 0; i < count; i++) for (const key of ['type', 'packet', 'delay', 'applyTo']) list.push(field(`settings.${path}${path === 'noises' ? '.' + i : ''}.${key}`, `${path} ${i + 1} · ${key}`));
      }
      for (let i = 0; i < (draft.settings?.finalRules || []).length; i++) for (const [key, type] of [['action'], ['network'], ['port'], ['ip', 'list'], ['blockDelay']]) list.push(field(`settings.finalRules.${i}.${key}`, `Final rule ${i + 1} · ${key}`, type));
    }
    if (draft.protocol === 'dns') {
      for (const [key, label, type] of [['rewriteNetwork', 'Rewrite network'], ['rewriteAddress', 'Rewrite address'], ['rewritePort', 'Rewrite port', 'number'], ['blockTypes', 'Legacy blockTypes', 'numbers']]) list.push(field(`settings.${key}`, label, type));
    }
    if (draft.protocol === 'loopback') sniffingFields(list, 'settings.sniffing');
    const stream = draft.streamSettings || {};
    list.push(field('sendThrough', 'Исходящий адрес (sendThrough)'), field('targetStrategy', 'Target strategy'), field('proxySettings.tag', 'Legacy proxySettings tag'), field('proxySettings.transportLayer', 'Legacy transportLayer', 'boolean'));
    if (!['blackhole', 'dns', 'loopback', 'wireguard'].includes(draft.protocol)) {
      list.push(field(`streamSettings.${'method' in stream ? 'method' : 'network'}`, 'Транспорт', 'select', ['raw', 'tcp', 'ws', 'websocket', 'grpc', 'httpupgrade', 'xhttp', 'splithttp', 'kcp', 'mkcp', 'hysteria', 'masque', 'xdrive']), field('streamSettings.security', 'Защита', 'select', ['none', 'tls', 'reality']));
      const network = stream.method || stream.network || '';
      const transport = ({ raw: 'rawSettings', tcp: 'tcpSettings', ws: 'wsSettings', websocket: 'wsSettings', grpc: 'grpcSettings', httpupgrade: 'httpupgradeSettings', xhttp: 'xhttpSettings', splithttp: 'splithttpSettings', kcp: 'kcpSettings', mkcp: 'kcpSettings', hysteria: 'hysteriaSettings', masque: 'masqueSettings', xdrive: 'xdriveSettings' })[network];
      if (['ws', 'websocket', 'httpupgrade', 'xhttp', 'splithttp'].includes(network)) list.push(field(`streamSettings.${transport}.path`, 'Путь'), field(`streamSettings.${transport}.host`, 'Host'));
      if (['ws', 'websocket', 'httpupgrade', 'xhttp', 'splithttp'].includes(network)) list.push(field(`streamSettings.${transport}.headers`, 'Headers (имя: значение)', 'map'));
      if (['xhttp', 'splithttp'].includes(network)) list.push(field(`streamSettings.${transport}.mode`, 'Режим XHTTP', 'select', ['auto', 'packet-up', 'stream-up', 'stream-one']));
      if (network === 'grpc') list.push(field('streamSettings.grpcSettings.serviceName', 'Service name'), field('streamSettings.grpcSettings.authority', 'Authority'), field('streamSettings.grpcSettings.multiMode', 'Multi mode', 'boolean'));
      if (['kcp', 'mkcp'].includes(network)) for (const [key, label, type] of [['seed', 'Seed'], ['mtu', 'MTU', 'number'], ['tti', 'TTI', 'number'], ['uplinkCapacity', 'Uplink capacity', 'number'], ['downlinkCapacity', 'Downlink capacity', 'number'], ['congestion', 'Congestion', 'boolean']]) list.push(field(`streamSettings.${transport}.${key}`, label, type));
      if (['raw', 'tcp'].includes(network)) list.push(field(`streamSettings.${transport}.header.type`, 'RAW header type', 'select', ['none', 'http']));
      if (['tls', 'reality'].includes(stream.security)) {
        const prefix = `streamSettings.${stream.security}Settings`;
        list.push(field(`${prefix}.serverName`, 'SNI'), field(`${prefix}.fingerprint`, 'Fingerprint'));
        if (stream.security === 'tls') for (const [key, label, type] of [['alpn', 'ALPN', 'list'], ['allowInsecure', 'Пропускать проверку сертификата', 'boolean'], ['minVersion', 'Минимальная TLS'], ['maxVersion', 'Максимальная TLS'], ['echConfigList', 'ECH config']]) list.push(field(`${prefix}.${key}`, label, type));
        else list.push(field(`${prefix}.${'password' in (stream.realitySettings || {}) ? 'password' : 'publicKey'}`, 'Ключ REALITY', 'password'), field(`${prefix}.shortId`, 'Short ID'), field(`${prefix}.spiderX`, 'Spider X'));
      }
      if (network === 'hysteria') for (const [key, label, type] of [['auth', 'Hysteria auth', 'password'], ['version', 'Hysteria версия', 'number'], ['udpIdleTimeout', 'UDP idle timeout', 'number']]) list.push(field(`streamSettings.hysteriaSettings.${key}`, label, type));
      if (network === 'masque') for (const [key, label, type] of [['host', 'Host'], ['path', 'Путь'], ['user', 'Пользователь'], ['pass', 'Пароль', 'password'], ['warp.privateKey', 'WARP private key', 'password'], ['warp.publicKey', 'WARP public key'], ['warp.address', 'WARP адреса', 'list']]) list.push(field(`streamSettings.masqueSettings.${key}`, label, type));
      if (network === 'xdrive') for (const [key, label, type] of [['service', 'Service'], ['remoteFolder', 'Remote folder'], ['secrets', 'Secrets', 'list'], ['concurrency', 'Concurrency', 'number']]) list.push(field(`streamSettings.xdriveSettings.${key}`, label, type));
      list.push(field('mux.enabled', 'Mux', 'boolean'), field('mux.concurrency', 'Mux concurrency', 'number'), field('mux.xudpConcurrency', 'XUDP concurrency', 'number'), field('mux.xudpProxyUDP443', 'XUDP UDP/443', 'select', ['reject', 'allow', 'skip']), field('streamSettings.sockopt.dialerProxy', 'Цепочка: тег следующего outbound'), field('streamSettings.sockopt.tcpFastOpen', 'TCP Fast Open', 'boolean'), field('streamSettings.sockopt.interface', 'Исходящий интерфейс'));
    }
    if (draft.protocol === 'dns') {
      if ('nonIPQuery' in (draft.settings || {})) list.push(field('settings.nonIPQuery', 'Legacy nonIPQuery', 'select', ['drop', 'skip', 'reject']));
      for (let i = 0; i < (draft.settings?.rules || []).length; i++) for (const [key, label, type, options] of [['action', 'Действие', 'select', ['direct', 'drop', 'return', 'hijack']], ['qType', 'QType (например, 1,28)'], ['domain', 'Домены', 'list'], ['rCode', 'RCode', 'number']]) list.push(field(`settings.rules.${i}.${key}`, `Правило ${i + 1} · ${label}`, type, options));
    }
    if (draft.protocol === 'wireguard') for (let i = 0; i < (draft.settings?.peers || []).length; i++) for (const [key, label, type] of [['publicKey', 'Публичный ключ'], ['preSharedKey', 'Предварительный ключ', 'password'], ['endpoint', 'Endpoint'], ['allowedIPs', 'Allowed IPs', 'list'], ['keepAlive', 'Keepalive', 'number']]) list.push(field(`settings.peers.${i}.${key}`, `Peer ${i + 1} · ${label}`, type));
    const basic = new Set((settings[draft.protocol] || []).map(([key]) => settingPath(draft, key, selection)));
    for (const path of ['streamSettings.network', 'streamSettings.method', 'streamSettings.security', 'streamSettings.tlsSettings.serverName', 'streamSettings.realitySettings.serverName', 'streamSettings.realitySettings.publicKey', 'streamSettings.realitySettings.password', 'streamSettings.realitySettings.shortId']) basic.add(path);
    return list.map(item => ({ ...item, advanced: !basic.has(item.path) && !item.path.startsWith('settings.peers.') && !item.path.startsWith('settings.rules.') }));
  }
  function sniffingFields(list, prefix) {
    for (const [key, label, type] of [['enabled', 'Enabled', 'boolean'], ['destOverride', 'Dest override', 'list'], ['domainsExcluded', 'Domains excluded', 'list'], ['ipsExcluded', 'IPs excluded', 'list'], ['metadataOnly', 'Metadata only', 'boolean'], ['routeOnly', 'Route only', 'boolean']]) list.push(field(`${prefix}.${key}`, `Sniffing · ${label}`, type));
  }
  function collections(draft) {
    const result = [];
    if (draft.protocol === 'wireguard') result.push({ path: 'settings.peers', label: 'Peer' });
    if (draft.protocol === 'dns' && !('nonIPQuery' in (draft.settings || {}))) result.push({ path: 'settings.rules', label: 'Правило DNS' });
    if (draft.protocol === 'freedom') result.push({ path: 'settings.noises', label: 'Noise' }, { path: 'settings.finalRules', label: 'Final rule' });
    return result;
  }
  function resize(draft, path, add) {
    const values = get(draft, path) || [];
    if (add) values.push({}); else values.pop();
    set(draft, path, values);
  }
  function read(draft, descriptor) {
    const value = get(draft, descriptor.path);
    if (descriptor.type === 'map') return Object.entries(value || {}).map(([key, entry]) => `${key}: ${entry}`).join('\n');
    if (descriptor.type === 'numbers') return (value || []).join(',');
    return descriptor.type === 'list' ? (Array.isArray(value) ? value.join('\n') : String(value || '')) : (value ?? (descriptor.type === 'boolean' ? false : ''));
  }
  function write(draft, descriptor, input) {
    let value = input;
    if (descriptor.type === 'numbers') { value = String(input).split(/[,\s]+/).filter(Boolean).map(Number); if (value.some(item => !Number.isInteger(item))) throw new Error('Введите целые числа через запятую.'); }
    else if (descriptor.type === 'map') value = Object.fromEntries(String(input).split('\n').filter(line => line.trim()).map(line => { const index = line.indexOf(':'); if (index < 1) throw new Error('Заголовок задаётся как имя: значение.'); return [line.slice(0, index).trim(), line.slice(index + 1).trim()]; }));
    else if (descriptor.type === 'list') value = String(input).split(/[\n,]+/).map(item => item.trim()).filter(Boolean);
    else if (descriptor.type === 'number') value = input === '' ? undefined : Number(input);
    else if (descriptor.type === 'boolean') value = Boolean(input);
    else if (input === '') value = undefined;
    set(draft, descriptor.path, value);
  }
  const api = { protocols, fields, read, write, get, set, collections, resize };
  if (typeof module !== 'undefined' && module.exports) module.exports = api; else root.OutboundEditor = api;
})(typeof window !== 'undefined' ? window : globalThis);
