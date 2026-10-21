package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestExplicitOutboundReplacementAndStrictEnvelope(t *testing.T) {
	handler, path := newMutationServer(t)
	before, _ := os.ReadFile(path)
	for _, tc := range []struct{ path, body, code string }{
		{"/v1/xray/outbounds/proxy/replace", `{"expectedGeneration":0,"outbound":{"protocol":"trojan"}}`, "replacement_confirmation_required"},
		{"/v1/xray/outbounds/proxy/replace", `{"expectedGeneration":0,"confirmReplacement":true,"uri":"trojan://secret","outbound":{"protocol":"trojan"}}`, "invalid_request"},
		{"/v1/xray/outbounds/proxy/replace", `{"expectedGeneration":0,"confirmReplacement":true,"outbound":{"protocol":"trojan","settings":{"password":"first","password":"second"}}}`, "invalid_request"},
		{"/v1/xray/outbounds/direct/replace", `{"expectedGeneration":0,"confirmReplacement":true,"outbound":{"protocol":"freedom"}}`, "outbound_protected"},
	} {
		response := call(handler, http.MethodPost, tc.path, tc.body)
		if errorCode(t, response) != tc.code {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("rejected replacement wrote config")
	}
	replacement := `{"expectedGeneration":0,"confirmReplacement":true,"outbound":{"protocol":"trojan","settings":{"address":"edge.example","port":443,"password":"fixture-secret"}}}`
	if generationOf(t, call(handler, http.MethodPost, "/v1/xray/outbounds/proxy/replace", replacement)) != 1 {
		t.Fatal("wrong generation")
	}
	selected := call(handler, http.MethodGet, "/v1/xray/outbounds/proxy", "")
	if !strings.Contains(selected.Body.String(), "fixture-secret") {
		t.Fatal("selected credentials unavailable")
	}
	for _, endpoint := range []string{"/v1/xray/outbounds", "/v1/xray/overview", "/v1/xray/config/summary"} {
		response := call(handler, http.MethodGet, endpoint, "")
		if strings.Contains(response.Body.String(), "fixture-secret") {
			t.Fatal("summary leaked credentials")
		}
	}
	refs := call(handler, http.MethodGet, "/v1/xray/outbounds/proxy/references", "")
	if !strings.Contains(refs.Body.String(), "routing.rules[0].outboundTag") {
		t.Fatal("routing reference lost")
	}
}

func TestOutboundTagsDoNotCollideWithDraftRoutes(t *testing.T) {
	handler, _ := newMutationServer(t)
	if generationOf(t, call(handler, http.MethodPost, "/v1/xray/outbound-drafts/apply", `{"expectedGeneration":0,"outbound":{"tag":"import","protocol":"freedom"}}`)) != 1 {
		t.Fatal("create failed")
	}
	if got := call(handler, http.MethodGet, "/v1/xray/outbounds/import", ""); got.Code != 200 {
		t.Fatal("tag collides with route")
	}
	if got := call(handler, http.MethodPut, "/v1/xray/outbounds/proxy/vless", `{"expectedGeneration":1,"uri":"vless://fixture-secret"}`); got.Code != 404 {
		t.Fatalf("superseded import-into-existing still available: %d", got.Code)
	}
}
