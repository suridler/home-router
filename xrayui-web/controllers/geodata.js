function createGeodataController({ controlEnabled, getGeodataStatus, saveGeodataSettings, updateGeodata }) {
  return {
    status: async (_, res) => res.json(await getGeodataStatus()),
    save: async (req, res) => {
      if (!controlEnabled) {
        res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
        return;
      }
      try {
        const settings = await saveGeodataSettings(req.body || {});
        res.json({ ok: true, settings });
      } catch (error) {
        res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_request', message: error.message });
      }
    },
    update: async (_, res) => {
      if (!controlEnabled) {
        res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
        return;
      }
      try { res.json({ ok: true, ...(await updateGeodata()), geodata: await getGeodataStatus() }); }
      catch (error) { res.status(error.statusCode || 503).json({ ok: false, code: error.code || 'geodata_update_failed', message: error.message }); }
    }
  };
}

module.exports = { createGeodataController };
