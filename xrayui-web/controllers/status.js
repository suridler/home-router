function createStatusController({ getRouterStatus, getRouterConfig, getErrorHistory }) {
  return {
    health: async (_, res) => {
      const state = await getRouterStatus();
      res.json({ ok: true, service: 'xrayui-web', status: state.status, xray: state.xray });
    },
    status: async (_, res) => res.json(await getRouterStatus()),
    config: async (_, res) => res.json(await getRouterConfig()),
    alerts: async (_, res) => res.json(await getErrorHistory())
  };
}

module.exports = { createStatusController };
