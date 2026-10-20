function createXrayController({ routerConfig, controlEnabled, controlService, getRouterStatus,
  validateConfiguration }) {
  const disabled = res => {
    if (controlEnabled) return false;
    res.status(503).json({ ok: false, message: 'control API is disabled; enable [web].control_enabled in helper and restart helper' });
    return true;
  };
  const failure = (res, error) => res.status(error.statusCode || 503).json({ ok: false, code: error.code || 'helper_unavailable', message: error.message });
  return {
    testConfig: async (_, res) => {
      try { await validateConfiguration(); res.json({ ok: true, message: 'Configuration is valid' }); } catch (error) { failure(res, error); }
    },
    action: async (req, res) => {
      if (disabled(res)) return;
      try { const result = await controlService(routerConfig.xrayUnit, req.params.action); const state = await getRouterStatus(); res.json({ ok: true, ...result, xray: state.xray, status: state.status }); } catch (error) { failure(res, error); }
    },
    restart: async (_, res) => {
      if (disabled(res)) return;
      try { const result = await controlService(routerConfig.xrayUnit, 'restart'); const state = await getRouterStatus(); res.json({ ok: true, ...result, xray: state.xray, status: state.status }); } catch (error) { failure(res, error); }
    },
    networkAction: async (req, res) => {
      if (disabled(res)) return;
      try { const result = await controlService('xray-tproxy-routing.service', req.params.action); const state = await getRouterStatus(); res.json({ ok: true, ...result, nft: state.nft, policyRule: state.policyRule }); } catch (error) { failure(res, error); }
    }
  };
}

module.exports = { createXrayController };
