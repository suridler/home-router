package api

import (
	"strings"
	"testing"
)

func TestTemplateStatusValidation(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	revision := strings.Repeat("c", 64)
	valid := TemplateStatus{
		TemplateID: "nftables", State: TemplateDifferent,
		TemplateHash: hashA, SystemHash: hashB, Revision: revision,
		Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid status rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*TemplateStatus)
	}{
		{name: "unknown state", mutate: func(status *TemplateStatus) { status.State = "unknown" }},
		{name: "empty ID", mutate: func(status *TemplateStatus) { status.TemplateID = "" }},
		{name: "bad revision", mutate: func(status *TemplateStatus) { status.Revision = "not-a-hash" }},
		{name: "uppercase hash", mutate: func(status *TemplateStatus) { status.TemplateHash = strings.Repeat("A", 64) }},
		{name: "different equal hashes", mutate: func(status *TemplateStatus) { status.SystemHash = hashA }},
		{name: "different missing side", mutate: func(status *TemplateStatus) { status.Summary.SystemAvailable = false }},
		{name: "failure code on valid state", mutate: func(status *TemplateStatus) { status.Summary.Code = "read_failed" }},
		{name: "invalid without side", mutate: func(status *TemplateStatus) { status.State = TemplateInvalid; status.Summary.Code = "invalid_syntax" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := valid
			test.mutate(&status)
			if err := status.Validate(); err != ErrInvalidTemplateStatus {
				t.Fatalf("Validate() error = %v; want ErrInvalidTemplateStatus", err)
			}
		})
	}
}

func TestTemplateStatusValidationAcceptsEveryState(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	revision := strings.Repeat("c", 64)
	statuses := []TemplateStatus{
		{TemplateID: "xray-config", State: TemplateSynchronized, TemplateHash: hashA, SystemHash: hashA, Revision: revision, Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true}},
		{TemplateID: "nftables", State: TemplateDifferent, TemplateHash: hashA, SystemHash: hashB, Revision: revision, Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true}},
		{TemplateID: "sysctl", State: TemplateMissing, SystemHash: hashB, Revision: revision, Summary: TemplateStatusSummary{SystemAvailable: true}},
		{TemplateID: "sysctl", State: TemplateSystemMissing, TemplateHash: hashA, Revision: revision, Summary: TemplateStatusSummary{TemplateAvailable: true}},
		{TemplateID: "sysctl", State: TemplateInvalid, TemplateHash: hashA, Revision: revision, Summary: TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true, InvalidSide: TemplateSideSystem, Code: "invalid_syntax"}},
		{TemplateID: "tproxy-unit", State: TemplateInvalid, Revision: revision, Summary: TemplateStatusSummary{InvalidSide: TemplateSideBoth, Code: "both_missing"}},
	}
	for _, status := range statuses {
		if err := status.Validate(); err != nil {
			t.Errorf("state %q rejected: %v", status.State, err)
		}
	}
}
