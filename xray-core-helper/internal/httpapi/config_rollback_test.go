package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type rollbackTestRestarter struct{ calls int }

func (r *rollbackTestRestarter) Restart(context.Context) error { r.calls++; return nil }

func TestAutomaticAdoptionAndExplicitRollbackHTTP(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	original := []byte(mutationFixture)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarter := &rollbackTestRestarter{}
	engine, err := transaction.NewEngine(path, store, acceptConfigValidator{}, restarter)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: path}, store).WithDriftReconciler(engine.ReconcileDrift)
	handler := NewServer("test", reader, engine, nil, ServerServices{ConfigRollback: engine}).Handler
	if err := os.WriteFile(path, append(original, []byte("\n// operator edit")...), 0600); err != nil {
		t.Fatal(err)
	}
	state := httptest.NewRecorder()
	handler.ServeHTTP(state, httptest.NewRequest(http.MethodGet, "/v1/xray/overview", nil))
	if state.Code != 200 || strings.Contains(state.Body.String(), "\"configDrift\":true") {
		t.Fatalf("status=%d %s", state.Code, state.Body.String())
	}
	if restarter.calls != 0 {
		t.Fatal("adoption restarted Xray")
	}
	info := httptest.NewRecorder()
	handler.ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/v1/xray/config/rollback", nil))
	var status transaction.RollbackStatus
	if err := json.Unmarshal(info.Body.Bytes(), &status); err != nil || !status.Available || status.Generation != 1 {
		t.Fatalf("rollback status=%s %v", info.Body, err)
	}
	for _, body := range []string{`{}`, `{"expectedGeneration":1,"extra":true}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/rollback", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("invalid body status=%d", response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/rollback", strings.NewReader(`{"expectedGeneration":0}`)))
	if response.Code != 409 {
		t.Fatalf("stale rollback status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/rollback", strings.NewReader(`{"expectedGeneration":1}`)))
	if response.Code != 200 || restarter.calls != 1 {
		t.Fatalf("rollback status=%d restart=%d", response.Code, restarter.calls)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatal("rollback did not restore original file")
	}
}
