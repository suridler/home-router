package xrayconfig

import (
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

const loggingFixture = `{
  // outside log remains
  "log": {
    // access destination
    "access": "/var/log/xray/previous-access.log",
    "error": "/var/log/xray/previous-error.log",
    "loglevel": "warning",
    "dnsLog": false,
    "custom": { "keep": "fixture-secret" }
  },
  "unknownRoot": true
}`

func loggingRequest(access, errorMode api.LogTargetMode, level string, dnsLog bool) api.LogConfigurationRequest {
	return api.LogConfigurationRequest{
		Access:   api.LogTarget{Mode: access},
		Error:    api.LogTarget{Mode: errorMode},
		Loglevel: level,
		DNSLog:   &dnsLog,
	}
}

func TestPlanLogConfigurationEveryModeLevelAndDNSCombination(t *testing.T) {
	modes := []api.LogTargetMode{api.LogTargetDisabled, api.LogTargetJournal, api.LogTargetFile}
	levels := []string{"debug", "info", "warning", "error", "none"}
	dnsValues := []bool{false, true}

	for _, accessMode := range modes {
		for _, errorMode := range modes {
			for _, level := range levels {
				for _, dnsLog := range dnsValues {
					name := string(accessMode) + "/" + string(errorMode) + "/" + level
					if dnsLog {
						name += "/dns-on"
					} else {
						name += "/dns-off"
					}
					t.Run(name, func(t *testing.T) {
						request := loggingRequest(accessMode, errorMode, level, dnsLog)
						updated, err := plan(t, PlanLogConfiguration(request), loggingFixture)
						if err != nil {
							t.Fatal(err)
						}
						parsed, err := parseSource([]byte(updated))
						if err != nil {
							t.Fatal(err)
						}
						logging := asMap(parsed.value["log"])
						if got, want := logging["access"], logTargetPath(accessMode, managedAccessLogPath); got != want {
							t.Errorf("access = %#v, want %#v", got, want)
						}
						if got, want := logging["error"], logTargetPath(errorMode, managedErrorLogPath); got != want {
							t.Errorf("error = %#v, want %#v", got, want)
						}
						if logging["loglevel"] != level || logging["dnsLog"] != dnsLog {
							t.Errorf("loglevel/dnsLog = %#v/%#v", logging["loglevel"], logging["dnsLog"])
						}
					})
				}
			}
		}
	}
}

func TestPlanLogConfigurationPreservesCommentsUnknownFieldsAndOrdering(t *testing.T) {
	request := loggingRequest(api.LogTargetFile, api.LogTargetDisabled, "debug", true)
	updated, err := plan(t, PlanLogConfiguration(request), loggingFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{
		"// outside log remains",
		"// access destination",
		`"custom": { "keep": "fixture-secret" }`,
		`"unknownRoot": true`,
	} {
		if !strings.Contains(updated, preserved) {
			t.Errorf("updated config lost %q:\n%s", preserved, updated)
		}
	}
	positions := []int{
		strings.Index(updated, `"access"`),
		strings.Index(updated, `"error"`),
		strings.Index(updated, `"loglevel"`),
		strings.Index(updated, `"dnsLog"`),
		strings.Index(updated, `"custom"`),
	}
	for index := 1; index < len(positions); index++ {
		if positions[index-1] < 0 || positions[index] <= positions[index-1] {
			t.Fatalf("managed field ordering changed: %v", positions)
		}
	}
}

func TestPlanLogConfigurationAddsMissingMembersToUncommentedLogObject(t *testing.T) {
	source := `{"log":{"custom":true},"keep":1}`
	updated, err := plan(t, PlanLogConfiguration(loggingRequest(api.LogTargetJournal, api.LogTargetFile, "info", false)), source)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSource([]byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	logging := asMap(parsed.value["log"])
	if logging["access"] != "" || logging["error"] != managedErrorLogPath ||
		logging["loglevel"] != "info" || logging["dnsLog"] != false || logging["custom"] != true {
		t.Fatalf("unexpected log object: %#v", logging)
	}
	if !strings.Contains(updated, `"keep":1`) {
		t.Fatalf("unrelated root field changed: %s", updated)
	}
}

func TestPlanLogConfigurationNoOpAndIdempotency(t *testing.T) {
	request := loggingRequest(api.LogTargetFile, api.LogTargetJournal, "warning", false)
	first, err := plan(t, PlanLogConfiguration(request), loggingFixture)
	if err != nil {
		t.Fatal(err)
	}
	planner := PlanLogConfiguration(request)
	edits, err := planner([]byte(first))
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 0 {
		t.Fatalf("second plan is not a no-op: %#v", edits)
	}
	second, err := transaction.ApplyEdits([]byte(first), edits)
	if err != nil || string(second) != first {
		t.Fatalf("idempotent apply changed config: error=%v", err)
	}

	matching := `{"log":{"access":"/var/log/xray/access.log","error":"","loglevel":"warning","dnsLog":false}}`
	edits, err = planner([]byte(matching))
	if err != nil || len(edits) != 0 {
		t.Fatalf("matching settings plan = %#v, error = %v", edits, err)
	}
}

func TestPlanLogConfigurationRejectsInvalidRequestsWithoutLeakingValues(t *testing.T) {
	const secretValue = "vless://fixture-private-value?token=fixture-token"
	tests := []struct {
		name    string
		request api.LogConfigurationRequest
		code    string
	}{
		{"invalid access mode", loggingRequest(api.LogTargetMode(secretValue), api.LogTargetJournal, "info", false), "invalid_log_mode"},
		{"invalid error mode", loggingRequest(api.LogTargetFile, "arbitrary-path", "info", false), "invalid_log_mode"},
		{"invalid level", loggingRequest(api.LogTargetFile, api.LogTargetFile, "trace", false), "invalid_loglevel"},
		{"missing dns boolean", api.LogConfigurationRequest{Access: api.LogTarget{Mode: api.LogTargetFile}, Error: api.LogTarget{Mode: api.LogTargetJournal}, Loglevel: "info"}, "invalid_dns_log"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanLogConfiguration(test.request)([]byte(loggingFixture))
			if err == nil || rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s), want %s", err, rejectCode(err), test.code)
			}
			if strings.Contains(err.Error(), secretValue) || strings.Contains(err.Error(), "fixture-token") {
				t.Fatalf("validation error leaked request content: %v", err)
			}
		})
	}
}

func TestPlanLogConfigurationRejectsMalformedOrAmbiguousLogObjects(t *testing.T) {
	tests := []struct {
		name   string
		source string
		code   string
	}{
		{"missing log object", `{"outbounds":[]}`, "invalid_config"},
		{"null log", `{"log":null}`, "invalid_config"},
		{"array log", `{"log":[]}`, "invalid_config"},
		{"wrong access type with secret", `{"log":{"access":{"id":"00000000-0000-4000-8000-000000000000"}}}`, "invalid_config"},
		{"wrong error type", `{"log":{"error":false}}`, "invalid_config"},
		{"wrong level type", `{"log":{"loglevel":1}}`, "invalid_config"},
		{"wrong dns type", `{"log":{"dnsLog":"true"}}`, "invalid_config"},
		{"duplicate log object", `{"log":{},"log":{}}`, "invalid_config"},
		{"duplicate managed member", `{"log":{"access":"","access":""}}`, "invalid_config"},
		{"malformed JSONC", `{"log":{"access":`, "invalid_config"},
		{"commented insertion is ambiguous", `{"log":{// operator comment
          "access": ""
        }}`, "logging_comment_ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanLogConfiguration(loggingRequest(api.LogTargetFile, api.LogTargetFile, "info", true))([]byte(test.source))
			if err == nil || rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s), want %s", err, rejectCode(err), test.code)
			}
			if strings.Contains(err.Error(), "00000000-0000-4000-8000-000000000000") || strings.Contains(err.Error(), "operator comment") {
				t.Fatalf("planning error leaked configuration content: %v", err)
			}
		})
	}
}

func TestValidateLogConfigurationRequestRedactsStableErrors(t *testing.T) {
	request := loggingRequest("leaked-path-/var/log/private-secret", api.LogTargetJournal, "info", true)
	err := ValidateLogConfigurationRequest(request)
	if err == nil {
		t.Fatal("invalid mode accepted")
	}
	code, message, status := transaction.ErrorInfo(err)
	if code != "invalid_log_mode" || message == "" || status != 400 {
		t.Fatalf("error info = %q, %q, %d", code, message, status)
	}
	if strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("error leaked invalid mode: %v", err)
	}
}
