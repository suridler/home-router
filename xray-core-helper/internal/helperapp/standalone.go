package helperapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Match the daemon's long-operation write timeout for in-process requests.
const inProcessOperationTimeout = 20 * time.Minute

const standaloneVersion = "standalone"

// SetupClient returns an HTTP client that dispatches the versioned helper API
// in-process. It never opens a Unix or TCP listener. If first-run setup is
// required the client initially serves setup endpoints; after a successful
// apply/adopt it reinitializes the full normal handler so activation,
// network-probe and safe-apply APIs remain available without the daemon.
//
// The returned close function releases stores and the current handler. It is
// safe to call more than once and must be called before starting the daemon.
func SetupClient(ctx context.Context, configPath string) (*http.Client, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("setup client context is required")
	}
	transport := newInProcessTransport(ctx, configPath, standaloneVersion, buildInProcessState)
	if err := transport.start(ctx); err != nil {
		return nil, nil, err
	}
	return &http.Client{Transport: transport, Timeout: inProcessOperationTimeout}, transport.Close, nil
}

type inProcessState struct {
	handler        http.Handler
	close          func()
	setupCompleted <-chan struct{}
}

type inProcessBuild func(context.Context, string, string) (*inProcessState, error)

type inProcessTransport struct {
	ctx        context.Context
	configPath string
	version    string
	build      inProcessBuild

	mu            sync.Mutex
	state         *inProcessState
	reinitialized bool
	closed        bool
}

func newInProcessTransport(ctx context.Context, configPath, version string, build inProcessBuild) *inProcessTransport {
	return &inProcessTransport{ctx: ctx, configPath: configPath, version: version, build: build}
}

func (transport *inProcessTransport) start(ctx context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.build == nil {
		return errors.New("in-process helper composition is unavailable")
	}
	state, err := transport.build(ctx, transport.configPath, transport.version)
	if err != nil {
		return err
	}
	transport.state = state
	return nil
}

// Close releases the active composition. No background scheduler or listener is
// owned by this transport.
func (transport *inProcessTransport) Close() {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return
	}
	transport.closed = true
	if transport.state != nil && transport.state.close != nil {
		transport.state.close()
	}
	transport.state = nil
}

func (transport *inProcessTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("in-process request is required")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed || transport.state == nil {
		return nil, errors.New("in-process helper is closed")
	}
	if transport.setupCompletedLocked() && !transport.reinitialized {
		if err := transport.reinitializeLocked(); err != nil {
			return nil, err
		}
	}
	// A real HTTP server always supplies a non-nil Body, including for GET.
	// Client requests may have nil Body. Preserve the caller's request while
	// providing the same contract to the in-process server middleware.
	serverRequest := request.Clone(request.Context())
	if serverRequest.Body == nil {
		serverRequest.Body = http.NoBody
	}
	recorder := newResponseRecorder()
	transport.state.handler.ServeHTTP(recorder, serverRequest)
	response := recorder.result(request)
	// The setup response must be delivered even if normal-mode composition
	// fails immediately; the next request retries reinitialization and reports
	// a stable failure if it still cannot start.
	if transport.setupCompletedLocked() && !transport.reinitialized {
		_ = transport.reinitializeLocked()
	}
	return response, nil
}

func (transport *inProcessTransport) setupCompletedLocked() bool {
	if transport.state == nil || transport.state.setupCompleted == nil {
		return false
	}
	select {
	case <-transport.state.setupCompleted:
		return true
	default:
		return false
	}
}

func (transport *inProcessTransport) reinitializeLocked() error {
	ctx := transport.ctx
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	state, err := transport.build(ctx, transport.configPath, transport.version)
	if err != nil {
		return fmt.Errorf("reinitialize helper application: %w", err)
	}
	previous := transport.state
	transport.state = state
	transport.reinitialized = true
	if previous != nil && previous.close != nil {
		previous.close()
	}
	return nil
}

// buildInProcessState selects setup or normal mode without any daemon-only
// lifecycle component.
func buildInProcessState(ctx context.Context, configPath, version string) (*inProcessState, error) {
	app, err := openApplication(ctx, configPath, version, false)
	if err != nil {
		return nil, err
	}
	if err := app.prepareSetup(ctx); err != nil {
		return nil, err
	}
	probe := app.setupStartup.Status()
	if probe.RecoveryRequired || probe.SetupRequired {
		bundle, err := app.buildSetup()
		if err != nil {
			return nil, err
		}
		return &inProcessState{
			handler:        bundle.handler,
			close:          bundle.close,
			setupCompleted: bundle.service.Completed(),
		}, nil
	}
	if err := app.restoreNetwork(); err != nil {
		return nil, err
	}
	setupState := app.normalSetupService()
	if setupState.Status().RecoveryRequired {
		bundle, err := app.buildSetup()
		if err != nil {
			return nil, err
		}
		return &inProcessState{
			handler:        bundle.handler,
			close:          bundle.close,
			setupCompleted: bundle.service.Completed(),
		}, nil
	}
	normal, err := app.buildNormal(ctx, setupState)
	if err != nil {
		return nil, err
	}
	if setupState.HandoffPending() {
		if err := setupState.CompleteHandoff(ctx); err != nil {
			normal.Close()
			return nil, errors.New("setup handoff status failed")
		}
	}
	return &inProcessState{handler: normal.handler, close: normal.Close}, nil
}

type responseRecorder struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: make(http.Header)}
}

func (recorder *responseRecorder) Header() http.Header {
	return recorder.header
}

func (recorder *responseRecorder) WriteHeader(status int) {
	if recorder.wroteHeader {
		return
	}
	recorder.status = status
	recorder.wroteHeader = true
}

func (recorder *responseRecorder) Write(data []byte) (int, error) {
	if !recorder.wroteHeader {
		recorder.WriteHeader(http.StatusOK)
	}
	return recorder.body.Write(data)
}

func (recorder *responseRecorder) Flush() {}

func (recorder *responseRecorder) result(request *http.Request) *http.Response {
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	header := recorder.header.Clone()
	payload := append([]byte(nil), recorder.body.Bytes()...)
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        header,
		Body:          newResponseBody(payload),
		ContentLength: int64(len(payload)),
		Request:       request,
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
}

type responseBody struct {
	*bytes.Reader
}

func newResponseBody(payload []byte) *responseBody {
	return &responseBody{Reader: bytes.NewReader(payload)}
}

func (body *responseBody) Close() error { return nil }

var _ http.RoundTripper = (*inProcessTransport)(nil)
