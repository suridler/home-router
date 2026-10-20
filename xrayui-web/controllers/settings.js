function createSettingsController({ controlEnabled, getSystemSettings, updateLogSettings, updateSystemSettings }) {
  const rejectDisabled = res => { if (controlEnabled) return false; res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' }); return true; };
  return {
    getSystem: async (_, res) => { try { res.json({ ok: true, ...await getSystemSettings() }); } catch (error) { res.status(error.statusCode || 503).json({ ok: false, code: error.code || 'system_settings_unavailable', message: error.message }); } },
    updateSystem: async (req, res) => { if (rejectDisabled(res)) return; try { const value = Number(req.body?.backup?.retainPerTemplate); res.json({ ok: true, ...await updateSystemSettings(value, req.body?.connectivity) }); } catch (error) { res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_settings', message: error.message }); } },
    prepareLogFile: async (req, res) => {
      if (!controlEnabled) {
        res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
        return;
      }
      res.status(410).json({ ok: false, code: 'endpoint_removed', message: 'log files are prepared automatically by the helper' });
    },
    updateLogs: async (req, res) => {
      if (!controlEnabled) {
        res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
        return;
      }
      try {
        res.json({ ok: true, ...await updateLogSettings(req.body || {}, req.body?.restart === true, req.body?.expectedGeneration) });
      } catch (error) {
        res.status(error.statusCode || 400).json({ ok: false, code: error.code || 'invalid_request', message: error.message });
      }
    }
  };
}

module.exports = { createSettingsController };
