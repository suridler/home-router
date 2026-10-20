package logreader

import (
	"context"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/commandrunner"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileHistoryPreservesIdenticalOccurrencesAndCompletesPartialLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "error.log")
	message := "2026/10/18 01:02:03.123456 [Error] same"
	if err := os.WriteFile(path, []byte(message+"\n"+message+"\n"+message), 0600); err != nil {
		t.Fatal(err)
	}
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Error: path}}, root)
	first, err := reader.ReadHistory(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].ID == first.Events[1].ID || first.Events[0].Time.IsZero() {
		t.Fatal(first.Events)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n")
	f.Close()
	next, err := reader.ReadHistory(context.Background(), time.Time{})
	if err != nil || len(next.Events) != 3 || next.Events[0].ID != first.Events[0].ID {
		t.Fatal(next, err)
	}
	if err = os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(message+"\n"), 0600)
	rotated, err := reader.ReadHistory(context.Background(), time.Time{})
	if err != nil || len(rotated.Events) != 1 || rotated.Events[0].ID == first.Events[0].ID {
		t.Fatal("rotation identity reused", rotated, err)
	}
}

type historyRunner struct {
	output []byte
	args   []string
}

func (r *historyRunner) Run(_ context.Context, _ string, args []string) ([]byte, error) {
	r.args = args
	return r.output, nil
}
func TestJournalHistoryUsesCursorIdentityAndTimestamp(t *testing.T) {
	runner := &historyRunner{}
	for _, cursor := range []string{"cursor-a", "cursor-b"} {
		line, _ := json.Marshal(map[string]string{"__CURSOR": cursor, "__REALTIME_TIMESTAMP": "1234567890000000", "MESSAGE": "[Error] same", "PRIORITY": "6"})
		runner.output = append(runner.output, append(line, '\n')...)
	}
	reader := New(&fixedProvider{})
	reader.journalRunner = runner
	snapshot, err := reader.ReadHistory(context.Background(), time.Time{})
	if err != nil || len(snapshot.Events) != 2 {
		t.Fatal(snapshot, err)
	}
	if snapshot.Events[0].ID == snapshot.Events[1].ID || snapshot.Events[0].Time.UnixMicro() != 1234567890000000 {
		t.Fatal("cursor or time lost")
	}
	if !strings.Contains(strings.Join(runner.args, " "), "--grep") {
		t.Fatal("unbounded info read")
	}
	runner.output = []byte(`{"MESSAGE":"[Error] no identity"}`)
	if _, err = reader.ReadHistory(context.Background(), time.Time{}); err == nil {
		t.Fatal("accepted unidentified journal event")
	}
}

func TestEmptyGrepResultIsNotACollectionFailure(t *testing.T) {
	if _, err := historyJournalOutput(commandrunner.Result{}, &commandrunner.Error{Failure: commandrunner.FailureExit, ExitCode: 1}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		result commandrunner.Result
		err    error
	}{
		{commandrunner.Result{CapturedStderrBytes: 20}, &commandrunner.Error{Failure: commandrunner.FailureExit, ExitCode: 1}},
		{commandrunner.Result{}, &commandrunner.Error{Failure: commandrunner.FailureExit, ExitCode: 2}},
		{commandrunner.Result{}, errors.New("unavailable")},
	} {
		if _, err := historyJournalOutput(test.result, test.err); err == nil {
			t.Fatal("real failure hidden")
		}
	}
}
