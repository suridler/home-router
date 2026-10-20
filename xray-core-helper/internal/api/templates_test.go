package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTemplateStatusJSONContractForEveryState(t *testing.T) {
	templateHash := strings.Repeat("a", 64)
	systemHash := strings.Repeat("b", 64)
	revision := strings.Repeat("c", 64)
	tests := []struct {
		name string
		item TemplateStatus
		want map[string]any
	}{
		{
			name: "synchronized",
			item: TemplateStatus{TemplateID: "xray-config", State: TemplateSynchronized, TemplateHash: templateHash, SystemHash: templateHash, Revision: revision,
				Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true}},
			want: statusJSON("xray-config", "synchronized", templateHash, templateHash, revision, true, true, "", ""),
		},
		{
			name: "different",
			item: TemplateStatus{TemplateID: "nftables", State: TemplateDifferent, TemplateHash: templateHash, SystemHash: systemHash, Revision: revision,
				Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true}},
			want: statusJSON("nftables", "different", templateHash, systemHash, revision, true, true, "", ""),
		},
		{
			name: "template missing",
			item: TemplateStatus{TemplateID: "sysctl", State: TemplateMissing, SystemHash: systemHash, Revision: revision,
				Summary: TemplateStatusSummary{SystemAvailable: true}},
			want: statusJSON("sysctl", "template_missing", "", systemHash, revision, false, true, "", ""),
		},
		{
			name: "system missing",
			item: TemplateStatus{TemplateID: "sysctl", State: TemplateSystemMissing, TemplateHash: templateHash, Revision: revision,
				Summary: TemplateStatusSummary{TemplateAvailable: true}},
			want: statusJSON("sysctl", "system_missing", templateHash, "", revision, true, false, "", ""),
		},
		{
			name: "invalid",
			item: TemplateStatus{TemplateID: "sysctl", State: TemplateInvalid, TemplateHash: templateHash, Revision: revision,
				Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true, InvalidSide: TemplateSideSystem, Code: "invalid_syntax"}},
			want: statusJSON("sysctl", "invalid", templateHash, "", revision, true, true, "system", "invalid_syntax"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.item)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("JSON = %#v; want %#v", got, test.want)
			}
			for _, forbidden := range []string{"path", "content", "modifiedAt", "owner", "mode"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("JSON exposes forbidden field %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestTemplateStatusResponseKeepsOrderedItems(t *testing.T) {
	response := TemplateStatusResponse{Templates: []TemplateStatus{
		{TemplateID: "xray-config", State: TemplateSynchronized, Revision: strings.Repeat("a", 64)},
		{TemplateID: "nftables", State: TemplateDifferent, Revision: strings.Repeat("b", 64)},
	}}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"templates":[{"templateId":"xray-config","state":"synchronized","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","summary":{"templateAvailable":false,"systemAvailable":false}},{"templateId":"nftables","state":"different","revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","summary":{"templateAvailable":false,"systemAvailable":false}}]}` {
		t.Fatalf("response JSON = %s", encoded)
	}
}

func statusJSON(id, state, templateHash, systemHash, revision string, templateAvailable, systemAvailable bool, invalidSide, code string) map[string]any {
	result := map[string]any{
		"templateId": id,
		"state":      state,
		"revision":   revision,
		"summary": map[string]any{
			"templateAvailable": templateAvailable,
			"systemAvailable":   systemAvailable,
		},
	}
	if templateHash != "" {
		result["templateHash"] = templateHash
	}
	if systemHash != "" {
		result["systemHash"] = systemHash
	}
	if invalidSide != "" {
		result["summary"].(map[string]any)["invalidSide"] = invalidSide
	}
	if code != "" {
		result["summary"].(map[string]any)["code"] = code
	}
	return result
}
