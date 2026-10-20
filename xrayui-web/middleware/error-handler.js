const { HelperClientError } = require('../services/helper-client');

function errorHandler(error, req, res, next) {
  if (res.headersSent) {
    next(error);
    return;
  }

  if (error instanceof HelperClientError) {
    res.status(error.statusCode).json({ ok: false, code: error.code, message: error.message });
    return;
  }

  console.error(`Unhandled request error for ${req.method} ${req.path}:`, error);
  res.status(500).json({ ok: false, message: 'Internal server error' });
}

module.exports = { errorHandler };
