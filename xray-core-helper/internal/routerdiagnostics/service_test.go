package routerdiagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/nativebinding"
	"strings"
	"testing"
)

type commandCall struct {
	executable string
	args       []string
}
type commandReply struct {
	output []byte
	err    error
}
type fakeRunner struct {
	replies []commandReply
	calls   []commandCall
}

func (r *fakeRunner) Run(_ context.Context, exe string, args []string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{exe, append([]string(nil), args...)})
	if len(r.calls) > len(r.replies) {
		return nil, errors.New("private unexpected command")
	}
	reply := r.replies[len(r.calls)-1]
	return reply.output, reply.err
}
func testService(r commandRunner) *Service {
	return &Service{runner: r, bindings: func(context.Context) ([]nativebinding.Check, error) { return nil, nil }}
}
func TestDiagnoseRunsOnlyIndependentObservationCommands(t *testing.T) {
	r := &fakeRunner{replies: []commandReply{{output: []byte("default via 192.0.2.1 dev private0\n")}, {output: []byte("active\n")}, {output: []byte("active\n")}}}
	result, err := testService(r).Diagnose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || len(result.Checks) != 5 {
		t.Fatal("unexpected diagnostic shape")
	}
	for i, command := range fixedCommands {
		if r.calls[i].executable != command.executable || !equalArgs(r.calls[i].args, command.args) {
			t.Fatal("command differs from allowlist")
		}
	}
	if result.Checks[0].Status != StatusOK || result.Checks[3].Status != StatusNotDeclared || result.Checks[4].Status != StatusUnchecked {
		t.Fatal("absent checks inferred routing state")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "192.0.2.1") || strings.Contains(string(raw), "private0") {
		t.Fatal("command output leaked")
	}
}
func TestCommandFailuresAreRedactedAndDistinct(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status CheckStatus
	}{
		{&commandrunner.Error{Failure: commandrunner.FailureStart}, StatusUnavailable},
		{context.Canceled, StatusCanceled},
		{context.DeadlineExceeded, StatusTimedOut},
		{errors.New("SECRET_OUTPUT"), StatusFailed},
	} {
		r := &fakeRunner{replies: []commandReply{{err: tt.err}, {output: []byte("inactive")}, {output: []byte("active")}}}
		result, err := testService(r).Diagnose(context.Background())
		if err != nil || result.Checks[0].Status != tt.status {
			t.Fatal("wrong status", err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "SECRET") {
			t.Fatal("failure leaked")
		}
	}
}
func TestCanceledContextRunsNoCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &fakeRunner{}
	result, err := testService(r).Diagnose(ctx)
	if err != nil || len(r.calls) != 0 || result.Checks[0].Status != StatusCanceled {
		t.Fatal(err)
	}
}
func TestDiagnosticAllowlistHasNoNativePolicyQueries(t *testing.T) {
	for _, tt := range []commandCall{
		{"/sbin/ip", []string{"rule"}},
		{"/sbin/ip", []string{"route", "show", "table", "100"}},
		{"/usr/sbin/nft", []string{"list", "ruleset"}},
		{systemctlPath, []string{"restart", "xray.service"}},
	} {
		if fixedCommand(tt.executable, tt.args) {
			t.Fatal("unscoped command accepted")
		}
	}
	for _, command := range fixedCommands {
		if !fixedCommand(command.executable, command.args) {
			t.Fatal("fixed observation rejected")
		}
	}
}
