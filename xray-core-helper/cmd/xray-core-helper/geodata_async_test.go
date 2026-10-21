package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestGeodataCLIWaitsUsingStatusWithoutRepeatingPOST(t *testing.T) {
	for _, state := range []string{"succeeded", "failed"} {
		t.Run(state, func(t *testing.T) {
			posts, polls := 0, 0
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				op := api.GeodataOperation{ID: "test-operation", State: "running"}
				if r.Method == http.MethodPost {
					posts++
					w.WriteHeader(202)
					_ = json.NewEncoder(w).Encode(api.GeodataUpdateResponse{Operation: op})
				} else {
					polls++
					if r.URL.Path != "/v1/xray/geodata/update/status" {
						t.Errorf("unexpected poll %s", r.URL.Path)
					}
					op.State, op.ErrorCode = state, "geodata_download_failed"
					_ = json.NewEncoder(w).Encode(api.GeodataStatusResponse{Operation: &op})
				}
			}))
			var out, stderr strings.Builder
			code := run([]string{"-config", writeCLIConfig(t, server.socket), "geodata", "update"}, &out, &stderr)
			if (code == 0) != (state == "succeeded") || posts != 1 || polls != 1 {
				t.Fatalf("state=%s exit=%d posts=%d polls=%d stderr=%s", state, code, posts, polls, stderr.String())
			}
			if !strings.Contains(out.String(), state) {
				t.Fatal("final operation not printed", out.String())
			}
		})
	}
}
