document.addEventListener('alpine:init', () => {
  Alpine.data('routerApp', () => ({
    activeView: 'status', activeSettingsTab: 'logs', selectedLogKind: 'error',
    status: null, geodata: null, templates: [], logs: [], trafficSamples: [], logMessage: '', geodataMessage: '', templateMessage: '', settingsMessage: '', configTestMessage: '', vlessMessage: '', requestError: '', requestErrorSource: '',
    homeEventLines: [], homeEventFilter: 'error', homeEventLoading: false, homeEventError: '', homeEventUpdatedAt: null, homeEventStale: false,
    proxyDomains: null, proxyDomainInput: '', proxyDomainMessage: '', proxyDomainLoading: false, proxyDomainPendingName: '',
    proxyDomainsStale: false, proxyDomainsUpdatedAt: null, proxyDomainRevision: 0,
    loading: { status: false, logs: false, geodata: false }, pendingAction: '', logDirty: false, geodataDirty: false,
    logDraft: { access: { mode: 'journal', path: '' }, error: { mode: 'journal', path: '' }, loglevel: 'warning', dnsLog: false, rotationChange: 'keep', restart: false },
    safeApply: null, safeApplyKnown: false, safeApplyError: '', safeApplyLoading: false, safeApplyNow: Date.now(),
    helperUpdate: null, updateCheck: null, updateLoading: false, updateChecking: false, updateActionBusy: false,
    updateMessage: '', updateError: '', updateReadError: '', updateTargetVersion: '', updatePolling: false,
    updateInitialCheckStarted: false, updatePollTimer: null, updateUncertain: false,
    get updateInfo() { return this.helperUpdate; },
    set updateInfo(value) { this.helperUpdate = value; },
    get updateStatus() { return this.helperUpdate; },
    set updateStatus(value) { this.helperUpdate = value; },
    statusStale: false, statusUpdatedAt: null, statusLoadError: '', templatesLoading: false, actionMessage: '',
    connectivity: null, connectivityOutbound: '', connectivityMessage: '',
    networkProbes: {}, networkProbeLoading: false, networkProbeMessage: '',
    setupConfigState: "", setupFingerprint: "", setupMode: false, setupToken: "", setupInterfaces: [], setupReplaceXray: false, setupInterface: "", setupAddress: "", setupPlan: null, setupMessage: "", setupComplete: false,
    setupOperation: null,
    setupRecoveryRequired: false,
    geodataDraft: {}, systemSettings: { backup: { retainPerTemplate: 10 }, connectivity: { url: 'https://www.google.com/generate_204', expectedStatus: 204, timeoutSeconds: 15 } },
    vlessImport: { tag: '', uri: '', preview: null, restart: true },
    newVless: { tag: '', uri: '', preview: null, restart: true },
    outboundRemoval: { tag: '', references: null, restart: true },
    outboundMessage: '',
    routingConditionFields: [
      { key: 'inboundTag', label: 'Входящие подключения', placeholder: 'tproxy-in (one per line)' },
      { key: 'domain', label: 'Домены', placeholder: 'geosite:example\ndomain:example.com' },
      { key: 'ip', label: 'IP / CIDR', placeholder: 'geoip:private\n192.0.2.0/24' },
      { key: 'port', label: 'Порты', placeholder: '80,443 or 1000-2000' },
      { key: 'network', label: 'Сеть', placeholder: 'tcp or udp' },
      { key: 'protocol', label: 'Протоколы', placeholder: 'http\ntls' }
    ],
    routingEditor: { open: false, mode: 'add', id: '', sourcePosition: null, ruleTag: '', conditions: {}, targetType: 'outbound', target: '', position: 1, restart: false },
    routingMessage: '',
    routingMenuIndex: null,

    navigationOpen: false,
    navigationGroups: [
      { label: 'Обзор', items: [['status', 'Главная', '◉'], ['network', 'Сеть и TProxy', '⇄']] },
      { label: 'Трафик', items: [['outbounds', 'Прокси-серверы', '↗'], ['routing', 'Маршрутизация', '⑂'], ['geodata', 'Геоданные', '◎']] },
      { label: 'Управление', items: [['xray', 'Сервис Xray', '▣'], ['templates', 'Системные шаблоны', '▤'], ['logs', 'Журналы', '≡'], ['settings', 'Настройки', '⚙']] }
    ],
    sectionTabs: { outbounds: 'outbounds', geodata: 'geodata', templates: 'templates' },
    viewTitle() { return this.navigationGroups.flatMap(group => group.items).find(item => item[0] === this.activeView)?.[1] || 'Главная'; },
    viewDescription() {
      return ({ status: 'Состояние сервера и действия, требующие внимания.', network: 'Интерфейс, режим TProxy и проверка соединения.', outbounds: 'Подключения, через которые Xray отправляет трафик.', routing: 'Какие домены и соединения направлять через прокси.', geodata: 'Источники и обновление списков доменов и IP-адресов.', xray: 'Проверка конфигурации и управление службой.', templates: 'Сравнение и применение системных конфигураций с подтверждением.', logs: 'Ошибки и принятые Xray соединения.', settings: 'Параметры helper и журналирования.' })[this.activeView] || '';
    },
    dashboardTitle() {
      if (this.statusStale) return 'Состояние шлюза не подтверждено';
      if (!this.status) return 'Получаем состояние шлюза…';
      return ({ ok: 'Основные проверки пройдены', warning: 'Есть замечания к настройке', error: 'Обнаружены проблемы' })[this.status.dashboard?.state] || 'Состояние пока не определено';
    },
    dashboardDescription() {
      if (this.statusStale) return 'Свежие данные недоступны. Ниже показано последнее полученное состояние; изменения временно недоступны.';
      if (!this.status) return 'Ожидаем ответ helper.';
      const issues = this.status.dashboard?.issues || [];
      return issues.length ? issues.map(issue => this.issueSummary(issue)).join(' · ') : 'Службы и конфигурация проверены. Доступ в интернет автоматически ещё не проверялся.';
    },
    canMutate() { return Boolean(this.status?.controlEnabled && !this.statusStale && !this.pendingAction && !this.safeApply && this.safeApplyKnown && !this.safeApplyError && !this.safeApplyLoading && !this.setupMode && !this.updateBlocksMutations()); },
    mutationBlockedReason() {
      if (this.updateBlocksMutations()) return 'Выполняется или не завершено обновление helper. Обычные изменения заблокированы до его завершения.';
      if (this.pendingAction) return 'Выполняется операция. Дождитесь результата.';
      if (this.safeApply?.rebootRequired) return 'Изменения заблокированы до ручной перезагрузки сервера.';
      if (this.safeApply?.recoveryRequired) return 'Сначала завершите восстановление.';
      if (this.safeApply) return 'Сначала подтвердите или откатите временные настройки.';
      if (!this.safeApplyKnown || this.safeApplyError) return 'Состояние безопасного применения не подтверждено. Изменения временно недоступны.';
      if (this.statusStale) return 'Нет свежих данных от helper. Изменения временно недоступны.';
      if (!this.status?.controlEnabled) return 'Панель работает в режиме просмотра: управление отключено в настройках helper.';
      return '';
    },
    trafficStatusLabel() {
      if (this.statusStale) return 'Нет свежих данных helper';
      const error = this.status?.trafficError || '';
      if (!this.status) return 'Получаем данные…';
      if (/second sample|not advanced/.test(error)) return 'Ожидаем два свежих измерения exporter';
      if (/baseline was reset|without advancing/.test(error)) return 'Счётчики изменились; собираем новые измерения';
      if (/stale/.test(error)) return 'Измерения exporter устарели';
      if (/unreachable|timed out|request failed/.test(error)) return 'Exporter недоступен';
      if (/URL/.test(error)) return 'Источник метрик отключён или не настроен';
      return 'Exporter пока не предоставил полные данные';
    },
    proxyLabel() { return ({ 'direct-placeholder': 'Прямой выход', configured: 'Подключение задано', missing: 'Не настроен' })[this.status?.proxyState] || 'Нет данных'; },
    componentTitle(component) { return component.title; },
    issueTitle(issue) { return ({ 'System observations': 'Сеть и TProxy', 'Error log': 'Журнал ошибок', 'Geodata': 'Геоданные' })[issue.title] || issue.title; },
    issueSummary(issue) {
      if (issue.title === 'Geodata' && issue.state === 'disabled') return 'Автообновление геоданных не настроено';
      return issue.message;
    },
    async init() {
      this.activeView = this.viewFromHash();
      window.addEventListener('hashchange', () => { this.activeView = this.viewFromHash(); });
      this.startUpdatePolling();
      await this.loadUpdateStatus();
      if (!this.updateInitialCheckStarted) {
        this.updateInitialCheckStarted = true;
        if (this.updateControlsEnabled()) this.checkForUpdate({ initial: true }).catch(() => {});
      }
      if (await this.loadSetup()) { window.setInterval(() => this.loadSetup(), 1500); return; }
      await Promise.all([this.loadStatus(), this.loadLogs(), this.loadGeodata(), this.loadProxyDomains(), this.loadTemplates(), this.loadSystemSettings(), this.loadSafeApply(), this.refreshHomeEvents()]);
      await this.loadNetworkProbes();
      window.setInterval(() => { this.safeApplyNow = Date.now(); }, 1000);
      window.setInterval(() => { this.loadStatus(); this.loadSafeApply(); }, 5000);
      window.setInterval(() => { this.refreshHomeEvents(); }, 10000);
    },
    viewFromHash() {
      const [view, tab] = window.location.hash.slice(1).split('/');
      if (this.sectionTabs[view]) this.activeSettingsTab = this.sectionTabs[view];
      if (view === 'settings') this.activeSettingsTab = ['logs', 'system'].includes(tab) ? tab : 'system';
      return this.navigationGroups.some(group => group.items.some(item => item[0] === view)) ? view : 'status';
    },
    navigateSettings(tab) { window.location.hash = '#settings/' + (tab === 'logs' ? 'logs' : 'system'); this.activeView = this.viewFromHash(); this.navigationOpen = false; },
    async probeConnectivity() {
      if (this.pendingAction || this.setupMode) return;
      this.pendingAction = 'connectivity';
      try { this.connectivity = await this.api('/api/diagnostics/connectivity', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({outboundTag:this.connectivityOutbound}), timeoutMs:90000}); this.connectivityMessage = 'Проверены API Xray и системный DNS; для выбранного сервера — TCP-порт. Это не проверка доступа к сайту или прохождения LAN-трафика.'; }
      catch(error) { this.connectivityMessage = error.message; }
      finally { this.pendingAction = ''; }
    },
    async loadNetworkProbes() {
      try { this.networkProbes = await this.api('/api/network/probe'); }
      catch (error) { this.networkProbeMessage = error.message; }
    },
    async runNetworkProbe(stage = 'manual', transactionId = '') {
      if (this.networkProbeLoading) return;
      if (stage === 'before') this.networkProbes = { ...this.networkProbes, before: null, after: null };
      if (stage === 'after') this.networkProbes = { ...this.networkProbes, after: null };
      this.networkProbeLoading = true; this.networkProbeMessage = 'Проверка DNS, шлюза и HTTPS…';
      try {
        const result = await this.api('/api/network/probe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ stage, ...(transactionId ? { transactionId } : {}) }), timeoutMs: 45000 });
        this.networkProbes = { ...this.networkProbes, latest: result, ...(stage === 'before' ? { before: result, after: null } : {}), ...(stage === 'after' ? { after: result } : {}) };
        this.networkProbeMessage = result.reachable ? 'HTTPS с сервера доступен. Решение о сохранении настроек остаётся за вами.' : 'HTTPS-проверка не подтвердила доступность. Можно повторить проверку, сохранить настройки или выполнить откат.';
        return result;
      } catch (error) { this.networkProbeMessage = `Проверка недоступна: ${error.message}. Применение настроек не повторяется. Вы можете подтвердить или откатить их.`; }
      finally { this.networkProbeLoading = false; }
    },
    networkProbeSummary(result) {
      if (!result) return 'Нет сохранённого результата проверки.';
      const state = value => ({ ok: 'доступен', failed: 'не подтверждён', not_measured: 'не проверен' })[value] || '—';
      const https = result.reachable ? 'сайт доступен' : ({ tls_failed: 'ошибка TLS', tcp_failed: 'ошибка TCP', unexpected_http_status: 'неожиданный HTTP-код', http_timed_out: 'таймаут ответа' })[result.https?.code] || 'доступность не подтверждена';
      return `${this.formatDate(result.checkedAt)} · DNS: ${state(result.dns?.status)} · шлюз ${result.gateway || 'не определён'}: ${state(result.gatewayPing?.status)} · HTTPS: ${https} (HTTP ${result.httpStatus || '—'}, ожидается ${result.expectedStatus}) · ${result.durationMs} мс`;
    },
    navigate(view) { window.location.hash = '#' + view; this.activeView = this.viewFromHash(); this.navigationOpen = false; if (this.status && this.activeView === 'status') this.refreshHomeEvents(); },
    async api(path, options = {}) {
      const setupHeaders = path.startsWith('/api/setup') && this.setupToken ? { 'X-Bootstrap-Token': this.setupToken } : {};
      const controller = new AbortController();
      const timeoutMs = options.timeoutMs || (options.method && options.method !== 'GET' ? 3 * 60 * 1000 : 15000);
      const { timeoutMs: _timeoutMs, ...fetchOptions } = options;
      const timeout = window.setTimeout(() => controller.abort(), timeoutMs);
      try {
        const response = await fetch(path, { ...fetchOptions, headers: { ...setupHeaders, ...(fetchOptions.headers || {}) }, signal: controller.signal });
        const type = response.headers.get('content-type') || '';
        const data = type.includes('application/json') ? await response.json() : { message: await response.text() || `Request failed (${response.status})` };
        if (!response.ok) {
          const error = new Error(data.message || `Request failed (${response.status})`);
          error.code = data.code || '';
          error.status = response.status;
          error.mutationMayHavePersisted = data.mutationMayHavePersisted === true;
          throw error;
        }
        if (this.requestErrorSource === path) { this.requestError = ''; this.requestErrorSource = ''; }
        return data;
      } catch (error) {
        const message = error.name === 'AbortError' ? 'Request timed out.' : error.message || 'Network request failed.';
        this.requestError = message; this.requestErrorSource = path;
        error.message = message;
        if (error.name === 'AbortError') { error.code = 'request_timeout'; error.status = 0; }
        throw error;
      } finally { window.clearTimeout(timeout); }
    },
    async loadSetup() {
      try {
        const data = await this.api('/api/setup');
        this.setupOperation = data.operation || null;
        this.setupRecoveryRequired = data.recoveryRequired === true;
        this.setupConfigState = data.configState; this.setupFingerprint = data.configFingerprint || "";
        if (this.setupOperation) this.setupMessage = `${this.setupOperation.status}: ${this.setupOperation.phase}${this.setupOperation.errorCode ? ` (${this.setupOperation.errorCode})` : ''}`;
        const wasSetup = this.setupMode;
        this.setupMode = data.setupRequired === true || data.recoveryRequired === true || data.status === 'applying';
        if (wasSetup && !this.setupMode) { this.setupToken = ''; this.setupComplete = true; window.location.reload(); }
        return this.setupMode;
      } catch (_) { this.setupMode = true; this.setupMessage = 'Состояние настройки недоступно. Ожидаем связь; не повторяйте применение.'; return true; }
    },
    async unlockSetup() { try { const data = await this.api('/api/setup/interfaces'); this.setupInterfaces = data.interfaces || []; const eligible = this.setupInterfaces.filter(item => !item.excluded);  this.setupMessage = 'Токен принят. Выберите параметры сети.'; } catch (error) { this.setupMessage = error.message; } },
    async buildSetupPlan() { if (this.pendingAction || this.setupRecoveryRequired) return; this.setupPlan = null; this.pendingAction = 'setup-plan'; try { const data = await this.api('/api/setup/plan', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interface: this.setupInterface, address: this.setupAddress, replaceXray: this.setupReplaceXray === true }) }); this.setupPlan = data.plan; this.setupMessage = 'Проверьте параметры Xray и список изменений. Перехват пока выключен.'; } catch (error) { this.setupMessage = error.message; } finally { this.pendingAction = ''; } },
    async applySetup() {
      if (this.pendingAction || this.setupRecoveryRequired || !this.setupPlan || this.setupOperation?.status === 'applying' || !window.confirm('Применить показанные изменения Xray и подготовить сеть? Xray будет запущен или перезапущен при изменении конфига либо неготовности принимающей части. Перехват пока останется выключенным. Проверьте существующий nftables локально; взаимодействие правил не проверяется.')) return;
      this.pendingAction = 'setup-apply';
      try { const result = await this.api('/api/setup/apply', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(this.setupPlan), timeoutMs:6*60*1000}); this.setupComplete = result.status === 'configured_direct'; if (this.setupComplete) this.setupToken = ''; }
      catch(error) { this.setupMessage = `${error.code || 'response_lost'}: ${error.message}. Checking shared status; apply is not retried.`; }
      finally { this.pendingAction = ''; await this.loadSetup(); }
    },
    async adoptExistingSetup() {
      if (this.pendingAction || this.setupRecoveryRequired || !this.setupFingerprint || !window.confirm('Подключить существующий Xray? Конфиг, сеть и сервис Xray не изменятся.')) return;
      this.pendingAction = 'setup-adopt';
      try { await this.api('/api/setup/adopt', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({configFingerprint:this.setupFingerprint})}); }
      catch(error) {this.setupMessage=error.message;}
      finally {this.pendingAction='';await this.loadSetup();}
    },
    async recoverSetup() { if (this.pendingAction) return; this.pendingAction = 'setup-recover'; try { await this.api('/api/setup/recover', {method:'POST', timeoutMs:6*60*1000}); } catch(error) {this.setupMessage=error.message;} finally {this.pendingAction='';await this.loadSetup();} },
    async loadStatus() {
      if (this.loading.status) return;
      this.loading.status = true;
      try {
        this.status = await this.api('/api/status');
        this.statusStale = false; this.statusLoadError = ''; this.statusUpdatedAt = new Date().toISOString();
        this.recordTraffic(this.status.trafficRates);
        if (!this.logDirty) this.applyLogSettings(this.status.logs || {});
        if (!this.vlessOutbounds().some(outbound => outbound.tag === this.vlessImport.tag)) this.vlessImport.tag = this.vlessOutbounds()[0]?.tag || '';
        if (!this.allOutbounds().some(outbound => outbound.tag === this.outboundRemoval.tag)) this.outboundRemoval.tag = this.allOutbounds()[0]?.tag || '';
      }
      catch (error) { this.statusStale = true; this.statusLoadError = error.message; } finally { this.loading.status = false; }
    },
    async loadLogs() {
      if (this.loading.logs) return;
      const kind = this.selectedLogKind;
      this.loading.logs = true;
      try { const data = await this.api(`/api/logs/${kind}?limit=200`); if (kind === this.selectedLogKind) { this.logs = data.lines || []; this.logMessage = this.logs.length ? '' : 'Нет записей.'; } }
      catch (error) { if (kind === this.selectedLogKind) { this.logs = []; this.logMessage = error.message; } }
      finally { this.loading.logs = false; if (kind !== this.selectedLogKind) await this.loadLogs(); }
    },
    async loadHomeEvents() {
      if (this.homeEventLoading) return;
      this.homeEventLoading = true;
      try {
        const data = await this.api('/api/logs/error?limit=100');
        this.homeEventLines = Array.isArray(data.lines) ? data.lines : [];
        this.homeEventUpdatedAt = new Date().toISOString();
        this.homeEventError = '';
        this.homeEventStale = false;
      } catch (error) {
        this.homeEventError = error.message || 'Не удалось загрузить события.';
        this.homeEventStale = true;
      } finally { this.homeEventLoading = false; }
    },
    refreshHomeEvents() {
      if (this.setupMode || this.activeView !== 'status' || this.homeEventFilter === 'error') return;
      if (document.hidden === true || document.visibilityState === 'hidden') return;
      return this.loadHomeEvents();
    },
    selectHomeEventFilter(filter) {
      if (!['error', 'all', 'warning', 'info'].includes(filter)) return;
      this.homeEventFilter = filter;
      if (filter !== 'error') return this.refreshHomeEvents();
    },
    errorHistoryStatus() {
      const history = this.status?.errorHistory;
      if (!history || history.unavailable) return 'История ошибок недоступна.';
      if (history.source === 'none') return 'Журнал ошибок отключён; показана ранее сохранённая история.';
      if (history.collectionError) return 'Сбор ошибок недоступен; показана ранее сохранённая история.';
      if (!history.collectedAt) return 'Ожидаем первый сбор ошибок helper.';
      const elapsed = Date.now() - Date.parse(history.collectedAt);
      if (!Number.isFinite(elapsed) || elapsed > 60000) return 'Сбор ошибок давно не обновлялся; показана сохранённая история.';
      return `Собрано: ${this.formatDate(history.collectedAt)}${history.truncated ? ' · Прочитано ограниченное окно журнала' : ''}`;
    },
    homeEventStatus() {
      if (this.homeEventError) {
        return this.homeEventUpdatedAt
          ? `Не удалось обновить события: ${this.homeEventError} Показаны данные на ${this.formatDate(this.homeEventUpdatedAt)}; обновление недоступно.`
          : `События недоступны: ${this.homeEventError}`;
      }
      return this.homeEventUpdatedAt ? `Обновлено: ${this.formatDate(this.homeEventUpdatedAt)}` : '';
    },
    filteredHomeEvents() {
      const lines = Array.isArray(this.homeEventLines) ? this.homeEventLines : [];
      const matching = this.homeEventFilter === 'all' ? lines : lines.filter(line => this.logLineSeverity(line) === this.homeEventFilter);
      return matching.slice().reverse().slice(0, 8);
    },
    async loadGeodata() {
      this.loading.geodata = true;
      try { const data = await this.api('/api/settings/geodata'); this.geodata = data; if (!this.geodataDirty) this.geodataDraft = { ...data.settings }; }
      catch (error) { this.geodataMessage = error.message; } finally { this.loading.geodata = false; }
    },
    validProxyDomainSnapshot(data) {
      return Boolean(data && ['effectiveDomains', 'permanentDomains', 'pendingAdditions', 'pendingDeletions'].every(key =>
        Array.isArray(data[key]) && data[key].every(value => typeof value === 'string')) && typeof data.consolidationActive === 'boolean');
    },
    async refreshProxyDomains() {
      const revision = ++this.proxyDomainRevision;
      try {
        const data = await this.api('/api/proxy-domains');
        if (revision !== this.proxyDomainRevision) return { superseded: true };
        if (!this.validProxyDomainSnapshot(data)) throw new Error('Некорректный ответ со списком записей.');
        this.proxyDomains = data;
        this.proxyDomainsStale = false;
        this.proxyDomainsUpdatedAt = new Date().toISOString();
        return { ok: true, data };
      } catch (error) {
        if (revision !== this.proxyDomainRevision) return { superseded: true };
        this.proxyDomainsStale = true;
        return { ok: false, error };
      }
    },
    async loadProxyDomains() {
      if (this.proxyDomainLoading) return;
      this.proxyDomainLoading = true;
      try {
        const result = await this.refreshProxyDomains();
        if (!result.superseded) this.proxyDomainMessage = result.ok ? '' : `Не удалось обновить список записей: ${result.error.message}`;
      } finally { this.proxyDomainLoading = false; }
    },
    proxyDomainStaleMessage() {
      if (!this.proxyDomains) return 'Список записей не получен: последнее чтение не удалось. Повторите обновление.';
      const at = this.proxyDomainsUpdatedAt ? ` на ${this.formatDate(this.proxyDomainsUpdatedAt)}` : '';
      return `Данные списка могли устареть: последнее обновление не удалось. Показаны сохранённые данные${at}.`;
    },
    proxyDomainMutationUncertain(error) {
      if (!error) return false;
      if (error.mutationMayHavePersisted === true) return true;
      if (error.code === 'request_timeout' || error.code === 'request_failed') return true;
      if (error.name === 'AbortError' || error.name === 'TypeError') return true;
      return !(Number(error.status) > 0) || Number(error.status) >= 500;
    },
    async proxyDomainMutationFailure(action, value, error) {
      if (!this.proxyDomainMutationUncertain(error)) return error.message;
      const refreshed = await this.refreshProxyDomains();
      const operation = action === 'delete' ? 'Удаление' : 'Добавление';
      const listState = refreshed.ok
        ? 'Список записей перечитан; проверьте актуальное состояние.'
        : 'Список записей перечитать не удалось; показаны последние полученные данные.';
      return `${operation} ${value} могло быть сохранено, но результат не подтверждён. ${error.message} ${listState} Действие автоматически не повторяется.`;
    },
    applyProxyDomainSnapshot(result) {
      if (!result || typeof result.domain !== 'string' || !result.domain || typeof result.applied !== 'boolean' || !this.validProxyDomainSnapshot(result.snapshot)) {
        throw new Error('Результат изменения списка не подтверждён: получен некорректный ответ.');
      }
      ++this.proxyDomainRevision;
      this.proxyDomains = result.snapshot;
      this.proxyDomainsStale = false;
      this.proxyDomainsUpdatedAt = new Date().toISOString();
      return true;
    },
    proxyDomainAddedMessage(result, value) {
      const domain = result?.domain || value;
      return result?.applied === true
        ? `Запись ${domain} добавлена в прокси-маршрутизацию и применена.`
        : `Запись ${domain} сохранена в списке прокси, но изменения ещё не применены к работающей конфигурации.`;
    },
    proxyDomainDeletedMessage(result, value) {
      const domain = result?.domain || value;
      if (result?.applied === true && result?.snapshot?.pendingDeletions?.includes(domain)) {
        return `Удаление записи ${domain} сохранено; запись останется в USERLIST до следующего обновления геобаз.`;
      }
      return result?.applied === true
        ? `Запись ${domain} удалена из динамического списка.`
        : `Удаление записи ${domain} сохранено, но изменения ещё не применены к работающей конфигурации.`;
    },
    async loadSystemSettings() { try { const data = await this.api("/api/settings/system"); this.systemSettings = { backup: { retainPerTemplate: data.backup?.retainPerTemplate || 10 }, connectivity: data.connectivity || this.systemSettings.connectivity }; } catch (error) { this.settingsMessage = error.message; } },
    async saveSystemSettings() {
      if (!this.canMutate()) return; this.pendingAction = "system-settings-save"; try { const retainPerTemplate = Number(this.systemSettings.backup.retainPerTemplate); const data = await this.api("/api/settings/system", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ backup: { retainPerTemplate }, connectivity: this.systemSettings.connectivity }) }); this.systemSettings = data; this.settingsMessage = "Настройки helper сохранены."; } catch (error) { this.settingsMessage = error.message; } finally { this.pendingAction = ""; } },
    async loadTemplates(refresh = false) {
      if (this.templatesLoading) return;
      this.templatesLoading = true;
      try { const data = await this.api(refresh ? '/api/templates/refresh' : '/api/templates', refresh ? { method: 'POST' } : {}); this.templates = data.templates || []; this.templateMessage = ''; }
      catch (error) { this.templateMessage = error.message; } finally { this.templatesLoading = false; }
    },
    async loadSafeApply() {
      if (this.safeApplyLoading || this.pendingAction) return;
      this.safeApplyLoading = true;
      try { const data = await this.api('/api/safe-apply'); this.safeApply = data.pending || null; this.safeApplyKnown = true; this.safeApplyError = ''; }
      catch (error) { this.safeApplyError = error.message; }
      finally { this.safeApplyLoading = false; }
    },
    safeApplySeconds() { const deadline = Date.parse(this.safeApply?.deadline); return Number.isFinite(deadline) ? Math.max(0, Math.ceil((deadline - this.safeApplyNow) / 1000)) : 0; },
    async resolveSafeApply(action) {
      if (!['confirm', 'rollback'].includes(action) || !this.safeApply || this.safeApply.rebootRequired || this.pendingAction || this.safeApplyLoading || this.safeApplyError) return;
      if (action === 'confirm' && (this.safeApply.recoveryRequired || this.safeApplySeconds() === 0)) return;
      const current = this.safeApply;
      this.pendingAction = "safe-apply-" + action;
      try {
        const data = await this.api("/api/safe-apply/" + encodeURIComponent(current.transactionId) + "/" + action, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
		this.safeApply = data.transaction?.rebootRequired || data.transaction?.recoveryRequired ? data.transaction : null;
        await this.loadTemplates(true);
        this.templateMessage = this.safeApply?.rebootRequired
          ? "REBOOT REQUIRED: nftables, sysctl and systemd files restored from one baseline; runtime effects are NOT restored. Reboot the server manually."
          : this.templateLabel(current.templateId) + (action === "confirm" ? ": настройки подтверждены." : ": настройки отменены.");
      } catch (error) { this.templateMessage = error.message; this.safeApplyError = error.message; }
      finally { this.pendingAction = ""; if (this.safeApplyError) await this.loadSafeApply(); }
    },
    templateLabel(id) { return ({ "xray-config": "Конфигурация Xray", nftables: "nftables", "tproxy-unit": "Сервис TProxy", sysctl: "Параметры sysctl" })[id] || id; },
    async acceptSystemSettings(row) {
      if (!this.canMutate() || row.state !== 'different') return;
      const label = this.templateLabel(row.templateId);
      if (!window.confirm(`Принять системный файл «${label}»? Текущий рабочий шаблон будет сохранён в резервной копии перед заменой.`)) return;
      this.pendingAction = `accept-system-${row.templateId}`;
      this.templateMessage = '';
      try {
        const backups = await this.api(`/api/templates/${encodeURIComponent(row.templateId)}/backups`);
        const result = await this.api(`/api/templates/${encodeURIComponent(row.templateId)}/accept-system`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ expectedGeneration: backups.completedGeneration, expectedRevision: row.revision })
        });
        await this.loadTemplates(true);
        this.templateMessage = result.accepted ? `${label}: системный файл принят, предыдущий шаблон сохранён в резервной копии.` : `${label}: уже синхронизирован.`;
      } catch (error) { this.templateMessage = error.message; }
      finally { this.pendingAction = ''; }
    },
    async applyTemplateSettings(row, activation = false) {
      if (!this.canMutate() || (row.state !== "different" && !(activation && row.templateId === "tproxy-unit" && row.state === "synchronized"))) return;
      if (row.templateId === 'tproxy-unit' && this.networkProbeLoading) { this.templateMessage = 'Дождитесь окончания проверки сети перед включением TProxy.'; return; }
      const label = this.templateLabel(row.templateId);
      const rebootWarning = ["nftables", "sysctl", "tproxy-unit"].includes(row.templateId)
        ? "\n\nВНИМАНИЕ: при откате восстанавливаются файлы nftables, sysctl и systemd из одной резервной копии операции. Затем требуется РУЧНАЯ ПЕРЕЗАГРУЗКА сервера. Автоматического reboot не будет. При потере SSH/web используйте консоль. Произвольные необратимые действия unit не отменяются."
        : "";
      const initializeBlock = row.templateId === "nftables" && row.summary?.blockInitializationRequired === true;
      if (initializeBlock && !window.confirm("Маркеры блока шлюза отсутствуют. Проверьте системный файл и новый блок локально. Добавить блок шлюза с сохранением существующих настроек? Взаимодействие правил автоматически не проверяется.")) return;
      if (!window.confirm("Применить шаблон «" + label + "»? Изменения будут временными и потребуют подтверждения." + rebootWarning)) return;
      this.pendingAction = "apply-template-" + row.templateId;
      this.templateMessage = "";
      this.safeApplyKnown = false;
      try {
        if (row.templateId === "tproxy-unit") await this.runNetworkProbe("before");
        const base = "/api/templates/" + encodeURIComponent(row.templateId);
        const backups = await this.api(base + "/backups");
        const applied = await this.api(base + "/apply", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expectedGeneration: backups.completedGeneration, ...(initializeBlock ? { initializeBlock: true, expectedRevision: row.revision } : {}) })
        });
        this.safeApplyKnown = true;
        this.safeApply = { transactionId: applied.transactionId, templateId: applied.templateId, deadline: applied.deadline, phase: applied.phase, state: applied.state, generation: applied.generation };
        this.templateMessage = label + ": применён временно. Подтвердите настройки в общем блоке выше до истечения таймера.";
        if (row.templateId === "tproxy-unit") await this.runNetworkProbe("after", applied.transactionId);
      } catch (error) { this.templateMessage = error.message; await this.loadSafeApply(); }
      finally { this.pendingAction = ""; if (!this.safeApplyKnown) await this.loadSafeApply(); }
    },
    templateStateLabel(state) { return ({ synchronized: "Синхронизирован", different: "Есть различия", template_missing: "Шаблон отсутствует", system_missing: "Системный файл отсутствует", invalid: "Требует исправления" })[state] || state; },
    async addProxyDomain(domain = null) {
      if (!this.canMutate()) return;
      const fromForm = domain === null;
      const submittedInput = this.proxyDomainInput;
      const value = String(fromForm ? submittedInput : domain).trim();
      if (!value) return;
      this.pendingAction = 'proxy-domain-add';
      ++this.proxyDomainRevision;
      this.proxyDomainMessage = '';
      try {
        const result = await this.api('/api/proxy-domains', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ domain: value })
        });
        this.applyProxyDomainSnapshot(result);
        if (fromForm && this.proxyDomainInput === submittedInput) this.proxyDomainInput = '';
        this.proxyDomainMessage = this.proxyDomainAddedMessage(result, value);
      } catch (error) {
        this.proxyDomainMessage = await this.proxyDomainMutationFailure('add', value, error);
      }
      finally { this.pendingAction = ''; }
    },
    async deleteProxyDomain(domain) {
      if (!this.canMutate()) return;
      const value = String(domain || '').trim();
      if (!value) return;
      this.pendingAction = 'proxy-domain-delete';
      ++this.proxyDomainRevision;
      this.proxyDomainPendingName = value;
      this.proxyDomainMessage = '';
      try {
        const result = await this.api(`/api/proxy-domains/${encodeURIComponent(value)}`, { method: 'DELETE' });
        this.applyProxyDomainSnapshot(result);
        this.proxyDomainMessage = this.proxyDomainDeletedMessage(result, value);
      } catch (error) {
        this.proxyDomainMessage = await this.proxyDomainMutationFailure('delete', value, error);
      }
      finally { this.pendingAction = ''; this.proxyDomainPendingName = ''; }
    },
    proxyDomainKind(domain) {
      return (this.proxyDomains?.permanentDomains || []).includes(domain) ? 'В основном списке' : 'Добавлен динамически';
    },
    applyLogSettings(logs) {
      const target = value => value === 'none' ? { mode: 'none', path: '' } : value ? { mode: 'file', path: value } : { mode: 'journal', path: '' };
      this.logDraft.access = target(logs.access); this.logDraft.error = target(logs.error); this.logDraft.loglevel = logs.loglevel || 'warning'; this.logDraft.dnsLog = logs.dnsLog === true;
    },
    async activateNetwork() {
      if (!this.canMutate()) return;
      await this.loadTemplates(true);
      const row = this.templates.find(item => item.templateId === 'tproxy-unit');
      if (!row) { this.requestError = 'Нет данных шаблона TProxy'; return; }
      return this.applyTemplateSettings(row, true);
    },
    async runAction(action) {
      if (!this.canMutate()) return;
      if (action === 'network-start' || action === 'network-restart') return this.activateNetwork();
      this.pendingAction = action; this.actionMessage = '';
      try { await this.api(action.startsWith('network-') ? `/api/network/${action.replace('network-', '')}` : `/api/xray/${action}`, { method: 'POST' }); await this.loadStatus(); this.actionMessage = 'Команда выполнена; состояние обновлено.'; }
      catch (error) { this.actionMessage = error.message; }
      finally { this.pendingAction = ''; }
    },
    async testConfig() {
      if (this.pendingAction || this.setupMode) return;
      this.pendingAction = 'config-test'; this.configTestMessage = 'Проверка конфигурации…';
      try { this.configTestMessage = (await this.api('/api/xray/config-test', { method: 'POST' })).message; }
      catch (error) { this.configTestMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async updateGeodata() {
      if (!this.canMutate()) return;
      this.pendingAction = 'geodata-update'; this.geodataMessage = 'Обновление геоданных…';
      try { await this.api('/api/geodata/update', { method: 'POST', timeoutMs: 15 * 60 * 1000 }); this.geodataMessage = ''; await Promise.all([this.loadGeodata(), this.loadStatus()]); }
      catch (error) { await Promise.all([this.loadGeodata(), this.loadStatus()]); this.geodataMessage = this.geodata?.failureLabel || error.message; } finally { this.pendingAction = ''; }
    },
    async saveGeodata() {
      if (!this.canMutate()) return;
      this.pendingAction = 'geodata-save';
      try { await this.api('/api/settings/geodata', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.geodataDraft) }); this.geodataDirty = false; this.geodataMessage = 'Настройки геоданных сохранены.'; await this.loadGeodata(); }
      catch (error) { this.geodataMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async saveLogs() {
      if (!this.canMutate()) return;
      this.pendingAction = 'logs-save';
      const payload = { access: { ...this.logDraft.access }, error: { ...this.logDraft.error }, loglevel: this.logDraft.loglevel, dnsLog: this.logDraft.dnsLog, restart: this.logDraft.restart, ...(this.logDraft.rotationChange === 'keep' ? {} : { rotation: { enabled: this.logDraft.rotationChange === 'enable' } }), expectedGeneration: this.status?.generation };
      const send = () => this.api('/api/settings/logs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      try {
        try { await send(); }
        catch (error) {
          if (error.code !== 'log_path_confirmation_required') throw error;
          if (!window.confirm('Ограничения systemd могут запрещать доступ к выбранному каталогу журналов, либо проверить доступ не удалось. Сохранить и применить настройку? Права службы не изменятся. Недоступные helper файлы потребуется подготовить вручную. Если Xray не запустится, настройка останется сохранённой — доступ потребуется исправить вручную.')) {
            this.settingsMessage = 'Изменение настроек журналов отменено.';
            return;
          }
          payload.confirmRestrictedPaths = true;
          await send();
        }
        this.logDirty = false; this.logDraft.rotationChange = 'keep'; this.settingsMessage = 'Настройки журналов сохранены.'; await this.loadStatus();
      }
      catch (error) {
        if (error.code === 'logging_saved_restart_failed') {
          this.logDirty = false; this.logDraft.rotationChange = 'keep';
          this.settingsMessage = 'Настройки журналов сохранены, но Xray не запустился. Проверьте права каталога и ограничения службы systemd. Автоматический откат не выполнялся.';
          await this.loadStatus();
        } else { this.settingsMessage = error.message; }
      } finally { this.pendingAction = ''; }
    },
    vlessOutbounds() { return (this.status?.outboundTags || []).filter(outbound => outbound.protocol === 'vless'); },
    allOutbounds() { return this.status?.outboundTags || []; },
    async previewVless() {
      if (this.pendingAction || this.setupMode) return;
      this.pendingAction = 'vless-preview'; this.vlessMessage = ''; this.vlessImport.preview = null;
      try {
        this.vlessImport.preview = await this.api('/api/outbounds/vless/preview', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uri: this.vlessImport.uri, tag: this.vlessImport.tag }) });
        this.vlessMessage = this.vlessImport.preview.unsupportedParameters?.length ? `Unsupported URI parameters: ${this.vlessImport.preview.unsupportedParameters.join(', ')}. They cannot be applied.` : 'Review the connection parameters, then apply the import.';
      } catch (error) { this.vlessMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async importVless() {
      if (!this.canMutate()) return;
      this.pendingAction = 'vless-import'; this.vlessMessage = '';
      try {
        const result = await this.api(`/api/outbounds/${encodeURIComponent(this.vlessImport.tag)}/import-vless`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uri: this.vlessImport.uri, restart: this.vlessImport.restart, expectedGeneration: this.status?.generation }) });
        this.vlessMessage = `Imported into '${result.outboundTag}'${result.restart ? ' and restarted Xray' : ''}.`;
        this.vlessImport.uri = ''; this.vlessImport.preview = null;
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.vlessMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async previewNewVless() {
      if (this.pendingAction || this.setupMode) return;
      this.pendingAction = 'vless-add-preview'; this.outboundMessage = ''; this.newVless.preview = null;
      try {
        this.newVless.preview = await this.api('/api/outbounds/vless/new/preview', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uri: this.newVless.uri, tag: this.newVless.tag }) });
        this.outboundMessage = this.newVless.preview.unsupportedParameters?.length ? `Unsupported URI parameters: ${this.newVless.preview.unsupportedParameters.join(', ')}. They cannot be applied.` : 'Review the new outbound, then add it.';
      } catch (error) { this.outboundMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async addVless() {
      if (!this.canMutate()) return;
      this.pendingAction = 'vless-add'; this.outboundMessage = '';
      try {
        const result = await this.api('/api/outbounds/vless', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uri: this.newVless.uri, tag: this.newVless.tag, restart: this.newVless.restart, expectedGeneration: this.status?.generation }) });
        this.outboundMessage = `Added '${result.outboundTag}'${result.restart ? ' and restarted Xray' : ''}.`;
        this.newVless.tag = ''; this.newVless.uri = ''; this.newVless.preview = null;
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.outboundMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async checkOutboundReferences() {
      if (this.pendingAction || this.setupMode) return;
      const tag = this.outboundRemoval.tag;
      this.pendingAction = 'outbound-reference-check'; this.outboundMessage = ''; this.outboundRemoval.references = null;
      try {
        const result = await this.api(`/api/outbounds/${encodeURIComponent(tag)}/references`);
        if (tag !== this.outboundRemoval.tag) return;
        this.outboundRemoval.references = result;
        this.outboundMessage = result.protected ? `'${result.outboundTag}' is required by the gateway profile.` : result.references.length ? `'${result.outboundTag}' is used by ${result.references.map(reference => reference.label).join(', ')}.` : `'${result.outboundTag}' is not used by routing and can be removed.`;
      } catch (error) { this.outboundMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async deleteOutbound() {
      if (!this.canMutate()) return;
      const tag = this.outboundRemoval.tag, restart = this.outboundRemoval.restart, generation = this.status?.generation;
      if (!tag) return;
      this.pendingAction = 'outbound-delete'; this.outboundMessage = '';
      try {
        const references = await this.api(`/api/outbounds/${encodeURIComponent(tag)}/references`);
        if (this.outboundRemoval.tag !== tag) throw new Error('Выбор подключения изменился. Повторите проверку для выбранного подключения.');
        this.outboundRemoval.references = references;
        if (references.protected || references.references.length) throw new Error(`'${references.outboundTag}' is still required by the gateway configuration`);
        const result = await this.api(`/api/outbounds/${encodeURIComponent(tag)}`, { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ restart, expectedGeneration: generation }) });
        this.outboundMessage = `Removed '${result.outboundTag}'${result.restart ? ' and restarted Xray' : ''}.`;
        this.outboundRemoval.references = null;
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.outboundMessage = error.message; } finally { this.pendingAction = ''; }
    },
    blankRoutingEditor(mode = 'add') {
      return { open: true, mode, id: '', sourcePosition: null, ruleTag: '', conditions: {}, targetType: 'outbound', target: '', position: (this.status?.routing?.rules?.length || 0) + 1, restart: false };
    },
    newRoutingRule() { this.routingMessage = ''; this.routingEditor = this.blankRoutingEditor('add'); },
    populateRoutingEditor(rule, mode) {
      const conditions = Object.fromEntries((rule.conditions || []).map(condition => [condition.key, condition.value.replace(/, /g, '\n')]));
      this.routingEditor = { ...this.blankRoutingEditor(mode), id: rule.tag || '', sourcePosition: mode === 'clone' ? rule.index : null, ruleTag: mode === 'clone' ? (rule.tag ? `${rule.tag}-copy` : '') : rule.tag, conditions, targetType: rule.balancerTag ? 'balancer' : 'outbound', target: rule.balancerTag || rule.outboundTag, position: rule.index + (mode === 'clone' ? 1 : 0) };
      this.routingMessage = '';
    },
    editRoutingRule(rule) { if (!rule.protected) this.populateRoutingEditor(rule, 'edit'); },
    cloneRoutingRule(rule) { this.populateRoutingEditor(rule, 'clone'); },
    closeRoutingEditor() { this.routingEditor.open = false; this.routingMessage = ''; },
    routingTargets() {
      return this.routingEditor.targetType === 'balancer'
        ? (this.status?.routing?.balancers || []).map(item => item.tag).filter(Boolean)
        : (this.status?.outboundTags || []).map(item => item.tag).filter(Boolean);
    },
    routingPositions() {
      const count = this.status?.routing?.rules?.length || 0;
      return Array.from({ length: count + 1 }, (_, offset) => ({ value: offset + 1, label: offset === count ? `После правила ${count}` : `Перед правилом ${offset + 1}` }));
    },
    routingPayload() {
      const conditions = Object.fromEntries(Object.entries(this.routingEditor.conditions).filter(([, value]) => String(value || '').trim()));
      return { type: 'field', ruleTag: this.routingEditor.ruleTag, conditions, outboundTag: this.routingEditor.targetType === 'outbound' ? this.routingEditor.target : '', balancerTag: this.routingEditor.targetType === 'balancer' ? this.routingEditor.target : '' };
    },
    routingPreview() {
      const rule = this.routingPayload();
      const conditions = Object.entries(rule.conditions).map(([key, value]) => `${this.routingConditionLabel(key)}: ${String(value).replace(/\n+/g, ', ')}`).join(' · ');
      return `${rule.ruleTag || 'Правило без названия'} · ${conditions || 'Без условий'} · ${this.routingEditor.targetType}: ${this.routingEditor.target || 'не выбрано'}`;
    },
    async saveRoutingRule() {
      if (!this.canMutate()) return;
      this.pendingAction = 'routing-save'; this.routingMessage = '';
      const editing = this.routingEditor.mode === 'edit';
      const cloning = this.routingEditor.mode === 'clone';
      const path = cloning ? '/api/routing/rules/clone' : editing ? `/api/routing/rules/${encodeURIComponent(this.routingEditor.id)}` : '/api/routing/rules';
      const body = cloning
        ? { position: this.routingEditor.sourcePosition, ruleTag: this.routingEditor.ruleTag, restart: this.routingEditor.restart, expectedGeneration: this.status?.generation }
        : { rule: this.routingPayload(), restart: this.routingEditor.restart, expectedGeneration: this.status?.generation };
      if (!editing && !cloning) body.position = this.routingEditor.position;
      try {
        const result = await this.api(path, { method: editing ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
        this.routingMessage = `Правило применено. Поколение конфигурации: ${result.generation}.`;
        this.routingEditor.open = false;
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.routingMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async moveRoutingRule(rule, direction) {
      if (!this.canMutate() || rule.protected || !this.canMoveRoutingRule(rule, direction) || !['up', 'down'].includes(direction)) return;
      this.pendingAction = 'routing-move';
      try {
        await this.api(`/api/routing/rules/${encodeURIComponent(rule.tag)}/move`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ direction, restart: false, expectedGeneration: this.status?.generation }) });
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.routingMessage = error.message; } finally { this.pendingAction = ''; }
    },
    async removeRoutingRule(rule) {
      if (!this.canMutate()) return;
      if (rule.protected || !rule.tag || !window.confirm(`Удалить правило «${rule.tag}»?`)) return;
      this.pendingAction = 'routing-delete';
      try {
        await this.api(`/api/routing/rules/${encodeURIComponent(rule.tag)}`, { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ restart: false, expectedGeneration: this.status?.generation }) });
        await this.loadStatus();
      } catch (error) { if (error.code === 'generation_conflict') await this.loadStatus(); this.routingMessage = error.message; } finally { this.pendingAction = ''; }
    },
    stateClass(state) { return `state--${state || 'unknown'}`; },
    stateLabel(state) { return ({ ok: 'В порядке', warning: 'Проверьте', error: 'Ошибка', disabled: 'Выключено', unknown: 'Нет данных', unverified: 'Не проверено' })[state] || 'Нет данных'; },
    systemRoutingRules() { return (this.status?.routing?.rules || []).filter(rule => rule.protected); },
    userRoutingRules() { return (this.status?.routing?.rules || []).filter(rule => !rule.protected); },
    routingConditionSummary(rule) {
      return (rule.conditions || []).map(condition => this.routingConditionLabel(condition.key)).join(' · ') || 'Без условий';
    },
    routingTargetLabel(rule) {
      return rule.outboundTag || rule.balancerTag || 'Направление не задано';
    },
    toggleRoutingMenu(index) { this.routingMenuIndex = this.routingMenuIndex === index ? null : index; },
    canMoveRoutingRule(rule, direction) {
      const rules = this.status?.routing?.rules || [];
      const targetIndex = rule.index + (direction === 'up' ? -1 : 1);
      if (targetIndex < 1 || targetIndex > rules.length) return false;
      return !rules[targetIndex - 1]?.protected;
    },
    routingConditionLabel(key) {
      return ({ inboundTag: 'Inbound', domain: 'Domain', ip: 'IP', port: 'Port', network: 'Сеть', protocol: 'Protocol', source: 'IP источника', sourcePort: 'Порт источника', user: 'Пользователь', email: 'Email', domainMatcher: 'Сопоставление доменов' })[key] || key;
    },
    routingActionHint(rule, action) {
      if (this.pendingAction) return 'Выполняется другая операция';
      if (rule.protected && action !== 'clone') return rule.protectionReason || 'Системное правило шлюза защищено';
      if (action === 'up' && rule.index === 1) return 'Правило уже первое';
      if (action === 'down' && rule.index === this.status?.routing?.rules?.length) return 'Правило уже последнее';
      return `${action[0].toUpperCase()}${action.slice(1)} rule`;
    },
    updateStorage() {
      try { return typeof window !== 'undefined' && window.sessionStorage ? window.sessionStorage : null; }
      catch (_) { return null; }
    },
    updateStorageRead(key) {
      try { return this.updateStorage()?.getItem(key) || ''; } catch (_) { return ''; }
    },
    updateStorageWrite(key, value) {
      try {
        const storage = this.updateStorage();
        if (!storage) return;
        if (value) storage.setItem(key, value); else storage.removeItem(key);
      } catch (_) { /* sessionStorage may be disabled or full */ }
    },
    updateOperation() { return this.helperUpdate?.operation || null; },
    updateState() { return this.updateOperation()?.state || ''; },
    updateTerminal(state = this.updateState()) { return state === 'succeeded' || state === 'rolled_back'; },
    updateIsActive(state = this.updateState()) { return ['preparing', 'launching', 'applying', 'verifying'].includes(state); },
    updateIsRecoverable(state = this.updateState()) { return state === 'failed' || state === 'interrupted'; },
    updateBlocksMutations() {
      const operation = this.updateOperation();
      return Boolean(operation) && operation.state !== 'prepared' && !this.updateTerminal(operation.state);
    },
    updateControlsEnabled() { return this.helperUpdate?.controlEnabled === true; },
    canPrepareUpdate(version = this.updateCheck?.latestVersion) {
      const operation = this.updateOperation();
      return Boolean(this.updateControlsEnabled() && !this.updateActionBusy && typeof version === 'string' && version &&
        this.updateCheck?.comparison === 'known' && this.updateCheck?.updateAvailable === true &&
        (!operation || this.updateTerminal(operation.state)));
    },
    canApplyPreparedUpdate() {
      return Boolean(this.updateControlsEnabled() && !this.updateActionBusy && this.updateState() === 'prepared' && this.updateOperation()?.id);
    },
    canRetryUpdate() {
      return Boolean(this.updateControlsEnabled() && !this.updateActionBusy && this.updateIsRecoverable() && this.updateOperation()?.id);
    },
    canRollbackUpdate() {
      return Boolean(this.canRetryUpdate() && this.updateOperation()?.snapshotReady === true);
    },
    updatePrimaryButtonVisible() { return this.canApplyPreparedUpdate() || this.canPrepareUpdate(); },
    updatePrimaryButtonLabel() {
      if (this.canApplyPreparedUpdate()) return `Установить подготовленную версию ${this.updateOperation()?.targetVersion || ''}`.trim();
      const target = this.updateCheck?.latestVersion || '';
      return target ? `Обновить до ${target}` : 'Обновить';
    },
    updateCurrentVersion() { return this.helperUpdate?.currentVersion || this.updateCheck?.currentVersion || '—'; },
    updateCurrentCommit() { return this.helperUpdate?.currentCommit || this.updateCheck?.currentCommit || ''; },
    updateLatestVersion() { return this.updateCheck?.latestVersion || '—'; },
    updateCheckMessage() {
      if (this.updateChecking) return 'Проверяем последний релиз GitHub…';
      if (this.updateReadError && !this.helperUpdate) return 'Состояние обновления недоступно. Повторите чтение статуса.';
      if (!this.updateCheck) return 'Проверка доступной версии ещё не выполнялась.';
      if (this.updateCheck.comparison !== 'known') return 'Не удалось сравнить версии; обновление не предлагается.';
      if (this.updateCheck.updateAvailable === true) return `Доступна версия ${this.updateCheck.latestVersion}.`;
      return 'Установлена последняя доступная версия.';
    },
    updateStateLabel() {
      if (!this.helperUpdate && this.updateReadError) return 'Нет связи';
      const state = this.updateState();
      const labels = {
        preparing: 'Подготовка комплекта',
        prepared: 'Подготовлено к установке',
        launching: 'Запуск установщика',
        applying: 'Установка',
        verifying: 'Проверка новой версии',
        failed: 'Ошибка установки',
        interrupted: 'Установка прервана',
        succeeded: 'Обновление завершено',
        rolled_back: 'Откат выполнен'
      };
      if (state) return labels[state] || state;
      if (!this.updateCheck) return 'Не проверено';
      return this.updateCheck.comparison === 'known' && this.updateCheck.updateAvailable ? 'Доступно обновление' : 'Проверено';
    },
    updateOperationDetail() {
      const operation = this.updateOperation();
      if (!operation) return '';
      const parts = [`Операция ${operation.id}`, `цель ${operation.targetVersion}`, `этап ${operation.stage || '—'}`];
      if (operation.error) parts.push(`ошибка ${operation.error}`);
      const attempts = operation.attempts || [];
      const last = attempts[attempts.length - 1];
      if (last) parts.push(`попытка ${last.action}: ${last.state}${last.error ? ` (${last.error})` : ''}`);
      return parts.join(' · ');
    },
    updateDisconnectedMessage() {
      if (!this.updateReadError) return '';
      if (this.updateIsActive() || this.updatePolling) {
        return `Связь с helper или панелью может прерываться во время установки (ожидается перезапуск helper и панели): ${this.updateReadError}. Показано последнее подтверждённое состояние журнала; чтение статуса повторяется каждые 3 секунды. Команда установки автоматически не повторяется.`;
      }
      return `Состояние обновления недоступно: ${this.updateReadError}`;
    },
    updateRecoveryMessage() {
      if (!this.updateIsRecoverable()) return '';
      return this.updateOperation()?.snapshotReady === true
        ? 'Установка не завершена. Журнал операции сохранён: можно повторить выбранную версию или откатить предыдущий комплект.'
        : 'Установка не завершена, снимок для отката не готов. Можно повторить установку выбранной версии; откат недоступен.';
    },
    newUpdateActionId() {
      const cryptoApi = (typeof window !== 'undefined' && window.crypto)
        || (typeof globalThis !== 'undefined' && globalThis.crypto);
      if (cryptoApi && typeof cryptoApi.randomUUID === 'function') return cryptoApi.randomUUID().replaceAll('-', '');
      if (cryptoApi && typeof cryptoApi.getRandomValues === 'function') {
        const bytes = new Uint8Array(16);
        cryptoApi.getRandomValues(bytes);
        return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
      }
      throw new Error('Secure random source is unavailable');
    },
    reconcileUpdateStorage(operation) {
      if (!operation?.id) {
        for (const key of ['operationId', 'actionId', 'action', 'targetVersion', 'uncertain']) this.updateStorageWrite(`xrayui.update.${key}`, '');
        this.updateUncertain = false;
        return;
      }
      const storedOperation = this.updateStorageRead('xrayui.update.operationId');
      if (storedOperation && storedOperation !== operation.id) {
        for (const key of ['actionId', 'action', 'uncertain']) this.updateStorageWrite(`xrayui.update.${key}`, '');
      }
      this.updateStorageWrite('xrayui.update.operationId', operation.id);
      if (operation.targetVersion) {
        this.updateTargetVersion = operation.targetVersion;
        this.updateStorageWrite('xrayui.update.targetVersion', operation.targetVersion);
      }
      const storedActionId = this.updateStorageRead('xrayui.update.actionId');
      const attempted = (operation.attempts || []).some(attempt => attempt.id === storedActionId);
      if (this.updateTerminal(operation.state)) {
        for (const key of ['operationId', 'actionId', 'action', 'uncertain']) this.updateStorageWrite(`xrayui.update.${key}`, '');
        this.updateUncertain = false;
      } else if (this.updateIsRecoverable(operation.state)) {
        for (const key of ['actionId', 'action', 'uncertain']) this.updateStorageWrite(`xrayui.update.${key}`, '');
        this.updateUncertain = false;
      } else {
        this.updateUncertain = this.updateStorageRead('xrayui.update.uncertain') === '1' && !attempted;
      }
    },
    ensureUpdateActionId(operation, action) {
      const storedOperation = this.updateStorageRead('xrayui.update.operationId');
      const storedAction = this.updateStorageRead('xrayui.update.action');
      const storedActionId = this.updateStorageRead('xrayui.update.actionId');
      const attempted = (operation.attempts || []).some(attempt => attempt.id === storedActionId);
      if (storedOperation === operation.id && storedAction === action && storedActionId && !attempted) {
        this.updateStorageWrite('xrayui.update.uncertain', '1');
        return storedActionId;
      }
      const actionId = this.newUpdateActionId();
      this.updateStorageWrite('xrayui.update.operationId', operation.id);
      this.updateStorageWrite('xrayui.update.action', action);
      this.updateStorageWrite('xrayui.update.actionId', actionId);
      this.updateStorageWrite('xrayui.update.uncertain', '1');
      return actionId;
    },
    startUpdatePolling() {
      if (this.updatePollTimer !== null || typeof window === 'undefined' || typeof window.setInterval !== 'function') return;
      this.updatePollTimer = window.setInterval(() => { this.pollUpdateStatus(); }, 3000);
    },
    async pollUpdateStatus() {
      if (!this.updatePolling && !this.updateIsActive()) return null;
      return this.loadUpdateStatus({ polling: true });
    },
    async loadUpdateStatus(options = {}) {
      if (this.updateLoading) return this.helperUpdate;
      this.updateLoading = true;
      try {
        const data = await this.api('/api/helper-update');
        this.helperUpdate = data || { controlEnabled: false, operation: null };
        this.updateReadError = '';
        this.reconcileUpdateStorage(this.helperUpdate.operation);
        if (this.updateIsActive()) {
          this.updatePolling = true;
          this.startUpdatePolling();
          this.updateMessage = 'Установка выполняется. Ожидается кратковременный перезапуск helper и панели; статус читается из журнала операции.';
        } else if (this.updateIsRecoverable()) {
          this.updatePolling = false;
          this.updateMessage = this.updateRecoveryMessage();
        } else if (this.updateTerminal()) {
          this.updatePolling = false;
          this.updateMessage = this.updateState() === 'rolled_back'
            ? `Откат завершён. Установлена версия ${this.updateCurrentVersion()}.`
            : `Обновление завершено. Установлена версия ${this.updateCurrentVersion()}.`;
        } else if (this.updateState() === 'prepared') {
          this.updatePolling = Boolean(this.updateUncertain);
          this.updateMessage = `Подготовлена версия ${this.updateOperation()?.targetVersion || ''}. Проверьте точную версию и подтвердите установку.`;
        } else {
          this.updatePolling = false;
        }
        return this.helperUpdate;
      } catch (error) {
        this.updateReadError = error.message || 'Не удалось прочитать состояние обновления.';
        // The journal is authoritative. A persisted operation means the page was
        // reloaded while helper/web were restarting, so keep reading status and
        // never replay the saved mutation automatically.
        const storedOperation = this.updateStorageRead('xrayui.update.operationId');
        const storedAction = this.updateStorageRead('xrayui.update.actionId');
        if (storedOperation || storedAction) {
          this.updatePolling = true;
          this.updateUncertain = this.updateStorageRead('xrayui.update.uncertain') === '1';
          this.updateTargetVersion = this.updateStorageRead('xrayui.update.targetVersion') || this.updateTargetVersion;
        } else if (this.updateIsActive()) {
          this.updatePolling = true;
        }
        return this.helperUpdate;
      } finally {
        this.updateLoading = false;
      }
    },
    async loadHelperUpdate() { return this.loadUpdateStatus(); },
    async checkForUpdate(options = {}) {
      if (this.updateChecking || this.updateActionBusy) return null;
      if (!this.updateControlsEnabled()) {
        this.updateError = 'Управление обновлениями отключено в настройках helper.';
        return null;
      }
      this.updateChecking = true;
      try {
        const data = await this.api('/api/helper-update/check', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({})
        });
        this.updateCheck = data;
        this.updateError = '';
        if (data?.currentVersion !== undefined) {
          this.helperUpdate = {
            ...(this.helperUpdate || {}),
            currentVersion: data.currentVersion,
            currentCommit: data.currentCommit,
            operation: this.helperUpdate?.operation || null
          };
        }
        return data;
      } catch (error) {
        this.updateError = `Не удалось проверить обновления: ${error.message}`;
        return null;
      } finally {
        this.updateChecking = false;
      }
    },
    async checkUpdate() { return this.checkForUpdate(); },
    async prepareUpdate(version = this.updateCheck?.latestVersion) {
      const operation = this.updateOperation();
      if (!this.updateControlsEnabled() || this.updateActionBusy || !version) return null;
      if (operation && !this.updateTerminal(operation.state)) return null;
      if (!window.confirm(`Подготовить обновление комплекта helper до версии ${version}? Будет загружен и проверен выбранный релиз; helper и панель пока продолжат работу.`)) return null;
      this.updateTargetVersion = version;
      this.updateActionBusy = true;
      this.pendingAction = 'helper-update-prepare';
      this.updateError = '';
      this.updateMessage = `Подготовка версии ${version}…`;
      try {
        const data = await this.api('/api/helper-update/prepare', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version })
        });
        const prepared = data?.operation || data;
        if (!prepared?.id) throw new Error('helper did not return a prepared operation');
        this.helperUpdate = { ...(this.helperUpdate || {}), operation: prepared };
        this.updateTargetVersion = prepared.targetVersion || version;
        this.updateStorageWrite('xrayui.update.operationId', prepared.id);
        this.updateStorageWrite('xrayui.update.targetVersion', this.updateTargetVersion);
        this.updateStorageWrite('xrayui.update.actionId', '');
        this.updateStorageWrite('xrayui.update.action', '');
        this.updateStorageWrite('xrayui.update.uncertain', '');
        this.updateUncertain = false;
        this.updatePolling = false;
        this.updateMessage = `Версия ${this.updateTargetVersion} подготовлена. Проверьте точную версию и подтвердите установку.`;
        return prepared;
      } catch (error) {
        this.updateError = `Не удалось подготовить обновление: ${error.message}`;
        this.updatePolling = true;
        await this.loadUpdateStatus({ polling: true });
        return null;
      } finally {
        this.updateActionBusy = false;
        this.pendingAction = '';
      }
    },
    async runUpdateMutation(action, operation) {
      if (!operation?.id || !this.updateControlsEnabled() || this.updateActionBusy) return null;
      this.startUpdatePolling();
      let actionId;
      try { actionId = this.ensureUpdateActionId(operation, action); }
      catch (error) { this.updateError = error.message; return null; }
      this.updateActionBusy = true;
      this.pendingAction = `helper-update-${action}`;
      this.updateError = '';
      this.updatePolling = true;
      this.updateMessage = action === 'rollback'
        ? `Запущен откат предыдущего комплекта…`
        : `Запущена установка версии ${operation.targetVersion}…`;
      try {
        const data = await this.api(`/api/helper-update/${action}`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ operationId: operation.id, actionId })
        });
        const next = data?.operation || data;
        if (next?.id) {
          this.helperUpdate = { ...(this.helperUpdate || {}), operation: next };
          this.updateTargetVersion = next.targetVersion || operation.targetVersion || this.updateTargetVersion;
          this.reconcileUpdateStorage(next);
          if (this.updateIsActive(next.state)) {
            this.updatePolling = true;
            this.updateMessage = 'Установка запущена. Helper и панель могут кратковременно отключиться; статус будет проверяться каждые 3 секунды без автоматического повтора команды.';
          } else if (this.updateTerminal(next.state)) {
            this.updatePolling = false;
            this.updateMessage = next.state === 'rolled_back'
              ? `Откат завершён. Установлена версия ${this.updateCurrentVersion()}.`
              : `Обновление завершено. Установлена версия ${this.updateCurrentVersion()}.`;
          } else if (this.updateIsRecoverable(next.state)) {
            this.updatePolling = false;
            this.updateMessage = this.updateRecoveryMessage();
          }
        }
        return next;
      } catch (error) {
        this.updateError = `Ответ на команду «${action}» не получен: ${error.message}. Команда автоматически не повторяется; состояние проверяется по журналу helper.`;
        // Status is the only automatic request after a lost mutation response.
        await this.loadUpdateStatus({ polling: true });
        return null;
      } finally {
        this.updateActionBusy = false;
        this.pendingAction = '';
      }
    },
    async applyUpdate() {
      const operation = this.updateOperation();
      if (!this.canApplyPreparedUpdate() || !operation) return null;
      if (!window.confirm(`Установить подготовленную версию ${operation.targetVersion}? Helper и панель будут перезапущены. Не закрывайте страницу до завершения проверки; при потере ответа установка не повторяется автоматически.`)) return null;
      return this.runUpdateMutation('apply', operation);
    },
    async retryUpdate() {
      const operation = this.updateOperation();
      if (!this.canRetryUpdate() || !operation) return null;
      if (!window.confirm(`Повторить установку версии ${operation.targetVersion} на сохранённый кандидат операции? Автоматического повтора не будет.`)) return null;
      return this.runUpdateMutation('retry', operation);
    },
    async rollbackUpdate() {
      const operation = this.updateOperation();
      if (!this.canRollbackUpdate() || !operation) return null;
      if (!window.confirm(`Откатить предыдущий комплект и данные? Текущая версия ${operation.targetVersion} будет заменена сохранённым снимком.`)) return null;
      return this.runUpdateMutation('rollback', operation);
    },
    async updatePrimaryAction() {
      if (this.canApplyPreparedUpdate()) return this.applyUpdate();
      if (this.canPrepareUpdate()) return this.prepareUpdate(this.updateCheck?.latestVersion);
      return null;
    },
    async runUpdateAction() { return this.updatePrimaryAction(); },
    formatDate(value) { return value ? new Date(value).toLocaleString('ru-RU') : '—'; },
    recordTraffic(rates) {
      if (!rates || this.statusStale || this.status?.trafficError || ![rates.in, rates.out].every(value => Number.isFinite(value) && value >= 0)) return;
      this.trafficSamples.push({ at: Date.now(), in: rates.in, out: rates.out });
      if (this.trafficSamples.length > 60) this.trafficSamples.shift();
    },
    trafficPoints(direction, width = 600, baseline = 112, amplitude = 100) {
      const values = this.trafficSamples.map(sample => direction === 'total' ? sample.in + sample.out : sample[direction]);
      const maximum = Math.max(...values, 1);
      const divisor = Math.max(values.length - 1, 1);
      return values.map((value, index) => `${(index / divisor) * width},${baseline - (value / maximum) * amplitude}`).join(' ');
    },
    trafficPeak() {
      const maximum = Math.max(0, ...this.trafficSamples.map(sample => sample.in + sample.out));
      return this.formatRate(maximum);
    },
    formatRate(value) {
      const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']; let unit = 0; let amount = value || 0;
      while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
      return `${amount >= 100 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
    },
    componentSummary(component) { return component.label; },
    componentAction(component) {
      return ({ xray: 'Управление службой →', system: 'Проверить сеть →', geodata: 'Настроить обновление →', errors: 'Открыть журнал →', helper: 'Настройки helper →' })[component] || 'Подробнее →';
    },
    errorLevel(line) { return this.logLineSeverity(line); },
    logLineSeverity(line) {
      const text = String(line || '');
      const marker = text.match(/\[(fatal|panic|error|err|warning|warn|info|debug|trace)\]/i)
        || text.match(/\b(?:level|severity|lvl)\s*[=:]\s*["']?(fatal|panic|error|err|warning|warn|info|debug|trace)\b/i);
      if (marker) {
        const level = marker[1].toLowerCase();
        if (['fatal', 'panic', 'error', 'err'].includes(level)) return 'error';
        if (['warning', 'warn'].includes(level)) return 'warning';
        return 'info';
      }
      const priority = text.match(/^<([0-7])>/) || text.match(/\bpriority[=:]\s*["']?([0-7])\b/i);
      if (priority) {
        if (Number(priority[1]) <= 3) return 'error';
        if (Number(priority[1]) === 4) return 'warning';
        return 'info';
      }
      if (/(?:^|:\s*)(?:fatal|panic)(?::|\s)/i.test(text)) return 'error';
      return 'info';
    },
    errorTime(line) {
      const text = String(line || '');
      const isoTimestamp = text.match(/^(\d{4}-\d{2}-\d{2}[T ][0-9:.]+(?:[+-]\d{2}:?\d{2}|Z)?)/);
      if (isoTimestamp) return isoTimestamp[1];
      const xrayTimestamp = text.match(/^(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})/);
      if (xrayTimestamp) return xrayTimestamp[1];
      const match = text.match(/^(\S+(?:\s+\S+){0,2})/);
      return match ? match[1] : 'recent';
    },
    openComponent(component) {
      const destinations = { helper: 'settings', xray: 'xray', config: 'xray', system: 'network', tproxy: 'network', connectivity: 'network', geodata: 'geodata', errors: 'logs', 'ready-templates': 'templates', 'ready-recovery': 'templates', 'ready-database': 'settings', 'ready-xray_api': 'xray', setup: 'network' };
      if (component === 'helper') this.activeSettingsTab = 'system';
      if (component === 'errors') { this.selectedLogKind = 'error'; this.loadLogs(); }
      this.navigate(destinations[component] || 'status');
    },
    markLogDirty() { this.logDirty = true; }, markGeodataDirty() { this.geodataDirty = true; }
  }));
});
