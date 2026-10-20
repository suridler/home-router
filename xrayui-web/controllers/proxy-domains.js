'use strict';

const safeErrorMessages = Object.freeze({
  invalid_domain: 'Укажите корректное DNS-имя или IPv4-адрес. IPv6, CIDR и URL не поддерживаются.',
  consolidation_active: 'Идёт консолидация geodata; изменение списка доменов и IPv4 временно заблокировано.',
  domain_runtime_sync_failed: 'Изменение могло быть сохранено, но синхронизировать его с работающим Xray не удалось. Проверьте список записей.',
  domain_runtime_apply_failed: 'Изменение могло быть сохранено, но применить его в работающем Xray не удалось. Проверьте список записей.',
  proxy_domain_unavailable: 'Служба доменов и IPv4 helper недоступна. Повторите попытку позже.',
  helper_timeout: 'Helper не ответил вовремя. Повторите попытку позже.',
  helper_unavailable: 'Helper недоступен. Сохранённые данные могут быть устаревшими.',
  helper_invalid_response: 'Helper вернул некорректный ответ. Повторите попытку позже.',
  helper_response_too_large: 'Ответ helper превысил допустимый размер.',
  invalid_request: 'Helper отклонил некорректный запрос.',
  control_disabled: 'Control API выключен в helper.'
});

const fallbackErrorMessage = 'Не удалось выполнить операцию со списком доменов и IPv4.';

function createProxyDomainsController({ controlEnabled, getProxyDomains, addProxyDomain, deleteProxyDomain }) {
  function respondWithError(res, error) {
    res.status(error.statusCode || 500).json({
      ok: false,
      code: error.code || 'proxy_domain_failed',
      message: Object.hasOwn(safeErrorMessages, error.code) ? safeErrorMessages[error.code] : fallbackErrorMessage,
      mutationMayHavePersisted: error.mutationMayHavePersisted === true
    });
  }

  function rejectWhenDisabled(res) {
    if (controlEnabled) return false;
    res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
    return true;
  }

  return {
    list: async (_, res) => {
      try {
        res.json({ ok: true, ...(await getProxyDomains()) });
      } catch (error) {
        respondWithError(res, error);
      }
    },
    add: async (req, res) => {
      if (rejectWhenDisabled(res)) return;
      try {
        res.json({ ok: true, ...(await addProxyDomain(req.body?.domain)) });
      } catch (error) {
        respondWithError(res, error);
      }
    },
    remove: async (req, res) => {
      if (rejectWhenDisabled(res)) return;
      try {
        res.json({ ok: true, ...(await deleteProxyDomain(req.params.domain)) });
      } catch (error) {
        respondWithError(res, error);
      }
    }
  };
}

module.exports = { createProxyDomainsController };
