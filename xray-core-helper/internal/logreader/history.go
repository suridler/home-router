package logreader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/commandrunner"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Event struct {
	ID       string
	Time     time.Time
	Message  string
	Source   string
	Priority string
}

var historyLevel = regexp.MustCompile(`(?i)\[(debug|info|warning|error|fatal|panic)\]`)
var historyPanic = regexp.MustCompile(`(?i)(^|:\s*)(fatal|panic):`)

// IsError respects explicit Xray severity rather than words inside a message.
func IsError(event Event) bool {
	if marker := historyLevel.FindStringSubmatch(event.Message); len(marker) > 0 {
		switch strings.ToLower(marker[1]) {
		case "error", "fatal", "panic":
			return true
		default:
			return false
		}
	}
	return historyPanic.MatchString(event.Message) || event.Priority == "0" || event.Priority == "1" || event.Priority == "2" || event.Priority == "3"
}

func eventID(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func fileEvents(tail []byte, info os.FileInfo, path string) []Event {
	events := make([]Event, 0, 10000)
	next := 0
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return events
	}
	offset := info.Size() - int64(len(tail))
	for _, raw := range bytes.SplitAfter(tail, []byte("\n")) {
		start := offset
		offset += int64(len(raw))
		// Do not ingest an unfinished write; its completed line is read next time.
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			continue
		}
		message := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if message == "" || !IsError(Event{Message: message}) {
			continue
		}
		event := Event{ID: eventID(fmt.Sprintf("%s:%d:%d:%s", path, stat.Ino, start, message)), Time: lineTime(message), Message: message, Source: "file"}
		if len(events) < 10000 {
			events = append(events, event)
		} else {
			events[next] = event
			next = (next + 1) % 10000
		}
	}
	if next > 0 {
		events = append(events[next:], events[:next]...)
	}
	return events
}

func lineTime(message string) time.Time {
	fields := strings.Fields(message)
	if len(fields) >= 2 {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05.999999999", fields[0]+" "+fields[1], time.Local); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (reader *Reader) readJournalHistory(ctx context.Context, since time.Time) (Result, error) {
	runner := reader.journalRunner
	if runner == nil {
		r, err := commandrunner.NewWithOutputLimit(journalctlPath, commandTimeout, MaxBytes)
		if err != nil {
			return Result{}, &Error{Code: ErrorJournalUnavailable}
		}
		runner = historyJournalRunner{runner: r}
	}
	// Filter before limiting so normal access/info messages do not displace errors.
	args := []string{"-u", xrayUnit, "-n", "200", "--no-pager", "-o", "json", "--grep", `\[(Error|Fatal|Panic)\]|(^|: )(fatal|panic):`, "--case-sensitive=no"}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Format("2006-01-02 15:04:05.000000")+" UTC")
	}
	output, err := runner.Run(ctx, journalctlPath, args)
	if err != nil {
		return Result{}, journalFailure(ctx, err)
	}
	if len(output) > MaxBytes {
		return Result{}, &Error{Code: ErrorOutputLimit}
	}
	result := Result{Kind: ErrorLog, Source: SourceJournal, Lines: []string{}, Events: []Event{}, BytesRead: len(output), ByteLimit: MaxBytes, LineLimit: 200}
	for _, line := range bytes.Split(output, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry struct {
			Cursor    string `json:"__CURSOR"`
			Timestamp string `json:"__REALTIME_TIMESTAMP"`
			Message   string `json:"MESSAGE"`
			Priority  string `json:"PRIORITY"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Cursor == "" {
			return Result{}, &Error{Code: ErrorJournalFailed}
		}
		micros, err := strconv.ParseInt(entry.Timestamp, 10, 64)
		if err != nil || micros < 0 {
			return Result{}, &Error{Code: ErrorJournalFailed}
		}
		result.Events = append(result.Events, Event{ID: eventID("journal:" + entry.Cursor), Time: time.UnixMicro(micros).UTC(), Message: entry.Message, Source: "journal", Priority: entry.Priority})
	}
	result.Truncated = len(result.Events) >= 200
	return result, nil
}

// journalctl --grep returns exit 1 without output when there are no matches.
// Other failures and any diagnostic output still surface as collection errors.
type historyJournalRunner struct{ runner *commandrunner.Runner }

func (runner historyJournalRunner) Run(ctx context.Context, executable string, args []string) ([]byte, error) {
	if executable != journalctlPath {
		return nil, &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	result, err := runner.runner.Run(ctx, args)
	return historyJournalOutput(result, err)
}
func historyJournalOutput(result commandrunner.Result, err error) ([]byte, error) {
	var failure *commandrunner.Error
	if errors.As(err, &failure) && failure.Failure == commandrunner.FailureExit && failure.ExitCode == 1 && result.CapturedStdoutBytes == 0 && result.CapturedStderrBytes == 0 {
		return []byte{}, nil
	}
	return result.Stdout(), err
}
