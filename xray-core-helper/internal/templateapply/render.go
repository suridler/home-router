package templateapply

import (
	"context"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativerender"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func (engine *Engine) SetNetwork(network bootstrap.NetworkConfig) {
	engine.network = network
	engine.renderEnabled = true
}
func (checker *CrossChecker) SetNetwork(network bootstrap.NetworkConfig) { checker.network = network }

func (engine *Engine) SetXrayValues(values xrayconfig.TemplateValues) { engine.xrayValues = &values }
func (checker *CrossChecker) SetXrayValues(values xrayconfig.TemplateValues) {
	checker.xrayValues = &values
}
func (engine *Engine) renderNative(ctx context.Context, raw []byte) ([]byte, error) {
	return (nativerender.Renderer{Network: engine.network, Catalog: engine.catalog}).Render(ctx, raw)
}

// SetLoggingValues supplies current helper settings for each render/check.
func (engine *Engine) SetLoggingValues(values func() bootstrap.LoggingConfig) {
	engine.loggingValues = values
}
func (checker *CrossChecker) SetLoggingValues(values func() bootstrap.LoggingConfig) {
	checker.loggingValues = values
}
