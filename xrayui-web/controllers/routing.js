'use strict';

function createRoutingController({ controlEnabled, addRoutingRule, cloneRoutingRule, deleteRoutingRule, editRoutingRule, moveRoutingRule }) {
  async function change(req, res, operation) {
    if (!controlEnabled) {
      res.status(403).json({ ok: false, message: 'Configuration changes are disabled' });
      return;
    }
    try {
      res.json({ ok: true, ...(await operation()) });
    } catch (error) {
      res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_request', message: error.message });
    }
  }
  return {
    add: (req, res) => change(req, res, () => addRoutingRule(req.body?.rule, req.body?.position, req.body?.restart === true, req.body?.expectedGeneration)),
    clone: (req, res) => change(req, res, () => cloneRoutingRule(req.body?.position, req.body?.ruleTag, req.body?.restart === true, req.body?.expectedGeneration)),
    edit: (req, res) => change(req, res, () => editRoutingRule(req.params.id, req.body?.rule, req.body?.restart === true, req.body?.expectedGeneration)),
    remove: (req, res) => change(req, res, () => deleteRoutingRule(req.params.id, req.body?.restart === true, req.body?.expectedGeneration)),
    move: (req, res) => change(req, res, () => moveRoutingRule(req.params.id, req.body?.direction, req.body?.restart === true, req.body?.expectedGeneration))
  };
}

module.exports = { createRoutingController };
