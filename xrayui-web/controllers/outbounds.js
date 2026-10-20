function createOutboundsController({ controlEnabled, addVlessOutbound, deleteOutbound, getOutboundReferences,
  previewNewVlessOutbound, previewVlessImport, importVlessOutbound }) {
  async function respond(res, operation) {
    try {
      res.json({ ok: true, ...(await operation()) });
    } catch (error) {
      res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_request', message: error.message });
    }
  }

  return {
    previewVless: async (req, res) => respond(res, () => previewVlessImport(req.body?.uri, req.body?.tag)),
    previewNewVless: async (req, res) => respond(res, () => previewNewVlessOutbound(req.body?.uri, req.body?.tag)),
    references: async (req, res) => respond(res, () => getOutboundReferences(req.params.tag)),
    importVless: async (req, res) => {
      if (!controlEnabled) {
        res.status(403).json({ ok: false, message: 'Configuration changes are disabled' });
        return;
      }
      await respond(res, () => importVlessOutbound(req.body?.uri, req.params.tag, req.body?.restart === true, req.body?.expectedGeneration));
    },
    addVless: async (req, res) => {
      if (!controlEnabled) {
        res.status(403).json({ ok: false, message: 'Configuration changes are disabled' });
        return;
      }
      await respond(res, () => addVlessOutbound(req.body?.uri, req.body?.tag, req.body?.restart === true, req.body?.expectedGeneration));
    },
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
