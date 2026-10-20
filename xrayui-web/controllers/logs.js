function createLogsController({ readLog }) {
  return {
    list: async (req, res) => {
      if (!['access', 'error'].includes(req.params.kind)) {
        res.status(400).json({ ok: false, message: 'kind must be access or error' });
        return;
      }
      const requested = Number.parseInt(req.query.limit, 10);
      const limit = Number.isFinite(requested) ? Math.min(Math.max(requested, 1), 200) : 200;
      try {
        const result = await readLog(req.params.kind, limit);
        res.json({ ok: true, kind: req.params.kind, limit, ...result });
      } catch (error) {
        res.status(503).json({ ok: false, message: error.message });
      }
    }
  };
}

module.exports = { createLogsController };
