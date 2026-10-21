package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayservice"
)

func TestNativeOperationsDoNotDependOnXrayPaths(t *testing.T) {
	for _, resolverFails := range []bool{false, true} {
		for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit, templatecatalog.XrayConfig} {
			t.Run(string(id)+"/"+map[bool]string{false: "changed", true: "unavailable"}[resolverFails], func(t *testing.T) {
				store, err := safeapply.Open(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				transaction, err := store.Begin(context.Background(), id, "backup", strings.Repeat("a", 64), 1, time.Now().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				// Identity remains available for idempotent requests after resolution.
				if _, _, err = store.Resolve(context.Background(), transaction.ID, safeapply.StateConfirmed); err != nil {
					t.Fatal(err)
				}
				paths := []string{
					"/v1/templates/" + string(id) + "/apply",
					"/v1/templates/" + string(id) + "/accept-system",
					"/v1/templates/" + string(id) + "/backups/backup/restore",
					"/v1/safe-apply/" + transaction.ID + "/confirm",
					"/v1/safe-apply/" + transaction.ID + "/rollback",
				}
				resolutions := 0
				handler := xrayservice.Guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), xrayservice.Installation{Config: "old.json"}, func(context.Context) (xrayservice.Installation, error) {
					resolutions++
					if resolverFails {
						return xrayservice.Installation{}, errors.New("unavailable")
					}
					return xrayservice.Installation{Config: "new.json"}, nil
				}, func(r *http.Request) bool { return IndependentNativeOperation(r, store.Transaction) })
				for _, path := range paths {
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
					expected := 204
					if id == templatecatalog.XrayConfig {
						expected = 409
					}
					if response.Code != expected {
						t.Fatalf("%s: got %d want %d", path, response.Code, expected)
					}
				}
				if id != templatecatalog.XrayConfig && resolutions != 0 {
					t.Fatalf("native operations resolved Xray %d times", resolutions)
				}
				for _, path := range []string{"/v1/safe-apply/unknown/confirm", "/v1/safe-apply/" + transaction.ID + "/cancel-reboot", "/v1/xray/config/adopt", "/v1/templates/unknown/apply"} {
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
					if response.Code != 409 {
						t.Fatalf("unexpected exemption %s: %d", path, response.Code)
					}
				}
			})
		}
	}
}

func TestNativeRecoveryStillChecksTransactionPhase(t *testing.T) {
	transaction := transactionFixture()
	service := &applyTemplateFake{transaction: transaction, err: safeapply.ErrPhase}
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: service})
	server.Handler = xrayservice.Guard(server.Handler, xrayservice.Installation{}, func(context.Context) (xrayservice.Installation, error) {
		t.Fatal("native recovery resolved Xray paths")
		return xrayservice.Installation{}, nil
	}, func(r *http.Request) bool {
		return IndependentNativeOperation(r, func(context.Context, string) (safeapply.Transaction, error) { return transaction, nil })
	})
	for _, action := range []string{"confirm", "rollback"} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/safe-apply/tx-1/"+action, nil))
		if response.Code != 409 || !strings.Contains(response.Body.String(), "safe_apply_phase_conflict") {
			t.Fatalf("%s: %d %s", action, response.Code, response.Body.String())
		}
	}
}
