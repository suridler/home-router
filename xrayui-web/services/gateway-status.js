'use strict';

// Report local observations separately from unmeasured end-to-end connectivity.
function gatewayStatus({ readiness, helperHealth, status, diagnostics, geodata, setup }) {
  const checks = diagnostics?.checks || [];
  const observation = name => checks.find(check => check.name === name)?.status;
  const configOK = status.configReadable === true && status.configParsed === true && status.configDrift !== true;
  const xray = observation('xray_service_active');
  const tproxy = observation('tproxy_service_active');
  const route = observation('default_route');
  const failures = checks.filter(check => !['xray_service_active', 'tproxy_service_active', 'default_route'].includes(check.name)
    && !['ok', 'unchecked', 'not_declared'].includes(check.status));
  const components = [
    { key: 'helper', title: 'Helper', state: helperHealth?.status === 'ok' ? 'ok' : 'unknown', label: helperHealth?.status === 'ok' ? 'API доступен' : 'Состояние helper не подтверждено' },
    { key: 'xray', title: 'Xray', state: geodata.activation ? 'error' : xray === 'ok' ? 'ok' : xray === 'mismatch' ? 'error' : 'unknown', label: geodata.activation ? geodata.activation.message : xray === 'ok' ? 'Служба работает' : xray === 'mismatch' ? 'Служба остановлена' : 'Состояние службы недоступно' },
    { key: 'config', title: 'Конфигурация', state: configOK ? 'ok' : 'error', label: configOK ? 'Конфиг читается, изменений вне helper не обнаружено' : 'Конфиг недоступен, не разобран или изменён вне helper' },
    { key: 'system', title: 'Сеть и TProxy', state: failures.length || route === 'mismatch' ? 'error' : route !== 'ok' ? 'unknown' : 'ok',
      label: failures.length ? 'Обнаружены расхождения в настройках сети' : route === 'ok' ? 'Маршрут по умолчанию найден' : route === 'mismatch' ? 'Нет маршрута по умолчанию' : 'Маршрут не проверен',
      checks: checks.filter(check => check.name !== 'xray_service_active') },
    { key: 'tproxy', title: 'Сервис TProxy', state: tproxy === 'ok' ? 'ok' : tproxy === 'mismatch' ? 'disabled' : 'unknown', label: tproxy === 'ok' ? 'Работает; прохождение LAN-трафика не проверено' : tproxy === 'mismatch' ? 'Выключен' : 'Состояние недоступно' },
    { key: 'geodata', title: 'Геоданные', state: geodata.health.state, label: geodata.health.state === 'disabled' ? 'Автообновление не настроено' : geodata.health.state === 'ok' ? 'Геобазы доступны' : geodata.health.label },
    { key: 'connectivity', title: 'Доступ в интернет', state: 'unverified', label: 'Сквозная проверка DNS/TLS/HTTP ещё не выполнена' }
  ];
  const known = new Set(configOK ? ['config'] : []);
  const readinessLabels = { database: 'База данных', recovery: 'Восстановление', templates: 'Системные шаблоны', xray_api: 'API Xray', config: 'Готовность конфигурации' };
  for (const check of readiness.checks || []) {
    if (known.has(check.name) && check.status === 'ready') continue;
    components.push({ key: `ready-${check.name}`, title: readinessLabels[check.name] || check.name, state: check.status === 'ready' ? 'ok' : 'error', label: check.status === 'ready' ? 'Проверка пройдена' : `Требуется проверка (${check.code || check.status})` });
  }
  if (!setup || setup.setupRequired || setup.recoveryRequired || setup.mutationsBlocked) {
    components.push({ key: 'setup', title: 'Первичная настройка', state: setup ? 'error' : 'unknown', label: setup ? 'Настройка или восстановление не завершены' : 'Состояние настройки недоступно' });
  }
  const state = readiness.status !== 'ready' || components.some(component => component.state === 'error') ? 'error'
    : components.some(component => component.state === 'unknown' || component.state === 'warning') ? 'warning' : 'ok';
  return { state, components, issues: components.filter(component => ['error', 'unknown', 'warning'].includes(component.state)).map(component => ({ key: component.key, title: component.title, message: component.label, state: component.state })) };
}

module.exports = { gatewayStatus };
