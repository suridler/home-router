function createOutboundsController({ controlEnabled, deleteOutbound, getOutboundReferences,
  importOutbound, editOutbound, writeOutbound }) {
  async function respond(res, operation) {
    try {
      res.json({ ok: true, ...(await operation()) });
    } catch (error) {
      res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_request', message: error.message });
    }
  }

  return {
    import: async (req, res) => { res.set('Cache-Control', 'no-store'); return respond(res, () => importOutbound(req.body?.uri)); },
    edit: async (req, res) => { res.set('Cache-Control', 'no-store'); return respond(res, () => editOutbound(req.params.tag)); },
    write: operation => async (req, res) => {
      if (!controlEnabled) return res.status(403).json({ ok: false, code: 'control_disabled', message: 'Configuration changes are disabled' });
      return respond(res, () => writeOutbound(operation, req.params.tag, req.body?.outbound, req.body?.restart === true, req.body?.expectedGeneration, req.body?.confirmReplacement === true));
    },
    references: async (req, res) => respond(res, () => getOutboundReferences(req.params.tag)),
    delete: async (req, res) => {
      if (!controlEnabled) {
        res.status(403).json({ ok: false, message: 'Configuration changes are disabled' });
        return;
      }
      await respond(res, () => deleteOutbound(req.params.tag, req.body?.restart === true, req.body?.expectedGeneration));
    }
  };
}

module.exports = { createOutboundsController };
