package xrayservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstallationUsesServiceBinaryConfigAndAssets(t *testing.T) {
	properties := map[string]any{
		"ExecStart":        [][]any{{"/opt/xray/xray", []string{"/opt/xray/xray", "run", "-config", "/etc/custom-xray/main.json"}, false, 0, 0, 0, 0, 0, 0, 0}},
		"Environment":      []string{"UNRELATED=private", "XRAY_LOCATION_ASSET=/var/lib/xray-core-helper/geodata"},
		"EnvironmentFiles": [][]any{}, "PassEnvironment": []string{}, "UnsetEnvironment": []string{},
	}
	r := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
		name := args[len(args)-1]
		kind, value := "s", any("")
		if name == "xray.service" {
			kind, value = "o", []string{"/org/freedesktop/systemd1/unit/xray_2eservice"}
		} else if v, ok := properties[name]; ok {
			value = v
			switch name {
			case "ExecStart":
				kind = "a(sasbttttuii)"
			case "EnvironmentFiles":
				kind = "a(sb)"
			default:
				kind = "as"
			}
		}
		return json.Marshal(map[string]any{"type": kind, "data": value})
	}}
	i, err := r.Resolve(context.Background())
	if err != nil || i.Binary != "/opt/xray/xray" || i.Config != "/etc/custom-xray/main.json" || i.AssetDir != "/var/lib/xray-core-helper/geodata" {
		t.Fatal(i, err)
	}
	properties["UnsetEnvironment"] = []string{"XRAY_LOCATION_ASSET"}
	if _, err = r.Resolve(context.Background()); err == nil {
		t.Fatal("ignored unset")
	}
	properties["UnsetEnvironment"] = []string{}
	properties["Environment"] = []string{}
	if _, err = r.Resolve(context.Background()); err == nil {
		t.Fatal("guessed missing assets")
	}
}

func TestEnvironmentAssetSubset(t *testing.T) {
	for _, text := range []string{"XRAY_LOCATION_ASSET=/usr/local/share/xray", "# comment\nXRAY_LOCATION_ASSET='/etc/xray/assets'", "XRAY_LOCATION_ASSET=\"/etc/xray/assets dir\""} {
		if _, found, err := assetAssignment(text); err != nil || !found {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"OTHER=\"unfinished\nXRAY_LOCATION_ASSET=/wrong", "XRAY_LOCATION_ASSET=$ASSETS", "XRAY_LOCATION_ASSET=relative", "XRAY_LOCATION_ASSET='/unterminated", "XRAY_LOCATION_ASSET=/one\\\n/two"} {
		if _, _, err := assetAssignment(text); err == nil {
			t.Fatal("accepted unsupported form")
		}
	}
}

func TestPathDriftBlocksMutationsButRetainsReadAccess(t *testing.T) {
	i := Installation{Unit: "xray.service", Config: "/etc/xray/a.json"}
	called := 0
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }), i, func(context.Context) (Installation, error) { return Installation{Config: "/etc/xray/b.json"}, nil })
	for _, test := range []struct {
		method string
		want   int
	}{{"GET", 204}, {"POST", 409}, {"PATCH", 409}} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(test.method, "/v1/setup/adopt", nil))
		if response.Code != test.want {
			t.Fatal(response.Code)
		}
	}
	if called != 1 {
		t.Fatal("mutation reached handler")
	}
}
