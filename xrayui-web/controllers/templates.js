'use strict';

function createTemplatesController({ controlEnabled, acceptSystemSettings, applyTemplateSettings, getSafeApply, resolveSafeApply, getTemplateBackups, getTemplateStatus, refreshTemplateStatus }) {
  const respond = (res, operation) => operation()
    .then(data => res.json({ ok: true, ...data }))
    .catch(error => res.status(error.statusCode || 503).json({ ok: false, code: error.code || 'template_status_unavailable', message: error.message }));
  return {
    status: (_, res) => respond(res, getTemplateStatus),
    refresh: (_, res) => respond(res, refreshTemplateStatus),
    backups: (req, res) => respond(res, () => getTemplateBackups(req.params.templateId)),
    acceptSystem: (req, res) => {
      if (!controlEnabled) { res.status(503).json({ ok: false, code: 'control_disabled', message: 'control API is disabled' }); return; }
      return respond(res, () => acceptSystemSettings(req.params.templateId, req.body?.expectedGeneration, req.body?.expectedRevision));
    },
    applyTemplate: (req, res) => {
      if (!controlEnabled) { res.status(503).json({ ok: false, code: "control_disabled", message: "control API is disabled" }); return; }
      const initialize = req.body?.initializeBlock !== undefined || req.body?.expectedRevision !== undefined;
      return respond(res, () => initialize
        ? applyTemplateSettings(req.params.templateId, req.body?.expectedGeneration, { initializeBlock: req.body?.initializeBlock, expectedRevision: req.body?.expectedRevision })
        : applyTemplateSettings(req.params.templateId, req.body?.expectedGeneration));
    },
    safeStatus: (_, res) => respond(res, getSafeApply),
    confirmSafeApply: (req, res) => respond(res, () => resolveSafeApply(req.params.transactionId, "confirm")),
    rollbackSafeApply: (req, res) => respond(res, () => resolveSafeApply(req.params.transactionId, "rollback"))
  };
}

module.exports = { createTemplatesController };
