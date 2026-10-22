package logreader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestReadConfiguredLogTailAndBoundaries(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "access.log")
	maximumLines := make([]string, MaxLines+1)
	for index := range maximumLines {
		maximumLines[index] = fmt.Sprintf("line-%03d", index)
	}
	maximumLinesContent := strings.Join(maximumLines, "\n")
	maximumLinesExpected := append([]string(nil), maximumLines[1:]...)
	largeFile := strings.Repeat("old\n", (MaxBytes*8)/4) + "latest-1\nlatest-2\n"
	tests := []struct {
		name      string
		content   string
		lineLimit int
		want      []string
		truncated bool
		byteCount int
	}{
		{
			name:      "large file returns latest bounded lines in order",
			content:   largeFile,
			lineLimit: 2,
			want:      []string{"latest-1", "latest-2"},
			truncated: true,
			byteCount: MaxBytes,
		},
		{
			name:      "no trailing newline",
			content:   "first\nlast",
			lineLimit: 10,
			want:      []string{"first", "last"},
			byteCount: len("first\nlast"),
		},
		{
			name:      "empty file",
			content:   "",
			lineLimit: 10,
			want:      []string{},
		},
		{
			name:      "line limit keeps latest lines",
			content:   "one\ntwo\nthree\nfour",
			lineLimit: 2,
			want:      []string{"three", "four"},
			truncated: true,
			byteCount: len("one\ntwo\nthree\nfour"),
		},
		{
			name:      "maximum line limit is enforced",
			content:   maximumLinesContent,
			lineLimit: MaxLines,
			want:      maximumLinesExpected,
			truncated: true,
			byteCount: len(maximumLinesContent),
		},
		{
			name:      "small file preserves complete first line",
			content:   "prefix\ntail",
			lineLimit: 200,
			want:      []string{"prefix", "tail"},
			byteCount: len("prefix\ntail"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			provider := &fixedProvider{logging: api.LoggingResponse{Access: logPath}}
			reader := readerForRoot(provider, root)
			result, err := reader.Read(context.Background(), Access, test.lineLimit)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(result.Lines) != fmt.Sprint(test.want) {
				t.Fatalf("lines = %#v, want %#v", result.Lines, test.want)
			}
			if result.Truncated != test.truncated {
				t.Errorf("truncated = %t, want %t", result.Truncated, test.truncated)
			}
			if result.BytesRead != test.byteCount {
				t.Errorf("bytes read = %d, want %d", result.BytesRead, test.byteCount)
			}
			if result.Source != SourceFile || result.ByteLimit != MaxBytes || result.LineLimit != test.lineLimit {
				t.Errorf("limits = (%d, %d), want (%d, %d)",
					result.ByteLimit, result.LineLimit, MaxBytes, test.lineLimit)
			}
		})
	}
}

func TestReadEnforcesByteLimitAndDropsOnlyPartialFirstLine(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "access.log")
	content := strings.Repeat("p", MaxBytes+100) + "\nkept-1\nkept-2"
	if err := os.WriteFile(logPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: logPath}}, root)
	result, err := reader.Read(context.Background(), Access, MaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesRead != MaxBytes || !result.Truncated {
		t.Fatalf("tail metadata = bytes %d, truncated %t; want %d and true",
			result.BytesRead, result.Truncated, MaxBytes)
	}
	if fmt.Sprint(result.Lines) != "[kept-1 kept-2]" {
		t.Fatalf("lines = %#v; want the complete lines after the partial first line", result.Lines)
	}

	if err := os.WriteFile(logPath, []byte(strings.Repeat("z", MaxBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = reader.Read(context.Background(), Access, MaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesRead != MaxBytes || result.Truncated || len(result.Lines) != 1 ||
		len(result.Lines[0]) != MaxBytes {
		t.Fatalf("exact byte boundary result = bytes %d, truncated %t, lines %d",
			result.BytesRead, result.Truncated, len(result.Lines))
	}
}

func TestReadPreservesCompleteLineAtByteWindowBoundary(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "access.log")
	tail := strings.Repeat("y", MaxBytes-7) + "\nkeep\n"
	content := "prefix\nx\n" + tail
	if len(tail) != MaxBytes-1 {
		t.Fatalf("test tail length = %d, want %d", len(tail), MaxBytes-1)
	}
	if err := os.WriteFile(logPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: logPath}}, root)
	result, err := reader.Read(context.Background(), Access, MaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 2 || result.Lines[0] != strings.Repeat("y", MaxBytes-7) ||
		result.Lines[1] != "keep" {
		t.Fatalf("lines = %#v; want complete window-boundary line and keep", result.Lines)
	}
}

func TestReadUsesCanonicalParsedConfigAsPathProvider(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "error.log")
	if err := os.WriteFile(logPath, []byte("earlier\nlatest"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	config := fmt.Sprintf(`{
		// The configured log location is read from canonical JSONC.
		"log": {"access": "none", "error": %q}
	}`, logPath)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	reader := readerForRoot(xrayconfig.NewReader(xrayconfig.Settings{Config: configPath}), root)
	result, err := reader.Read(context.Background(), ErrorLog, 3)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(result.Lines) != "[earlier latest]" || result.Kind != ErrorLog || result.Source != SourceFile {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadUsesJournalForMissingCanonicalLogPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"log":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeJournalRunner{output: []byte("journal-line")}
	reader := New(xrayconfig.NewReader(xrayconfig.Settings{Config: configPath}))
	reader.journalRunner = fake

	result, err := reader.Read(context.Background(), Access, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != SourceJournal || result.Kind != Access ||
		fmt.Sprint(result.Lines) != "[journal-line]" {
		t.Fatalf("journal result = %#v", result)
	}
}

func TestReadRejectsInvalidKindsPathsAndLineLimits(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret-token.log")
	provider := &fixedProvider{logging: api.LoggingResponse{Access: outside}}
	reader := readerForRoot(provider, root)
	reader.journalRunner = &fakeJournalRunner{}

	tests := []struct {
		name      string
		kind      Kind
		lineLimit int
		code      ErrorCode
	}{
		{"invalid kind", Kind("journal"), 3, ErrorInvalidKind},
		{"negative lines", Access, -1, ErrorInvalidLimit},
		{"zero lines", Access, 0, ErrorInvalidLimit},
		{"too many lines", Access, MaxLines + 1, ErrorInvalidLimit},
		{"outside root", Access, 3, ErrorInvalidPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := reader.Read(context.Background(), test.kind, test.lineLimit)
			requireErrorCode(t, err, test.code)
			if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), "secret-token") {
				t.Errorf("error exposed configured path: %v", err)
			}
		})
	}
	if provider.calls != 1 {
		t.Errorf("provider calls = %d; invalid kind/limits must be rejected before path lookup", provider.calls)
	}

	for _, invalid := range []string{
		"relative.log",
		root + "/../outside.log",
		root + "/./access.log",
	} {
		t.Run("path "+invalid, func(t *testing.T) {
			provider.logging.Access = invalid
			_, err := reader.Read(context.Background(), Access, 3)
			requireErrorCode(t, err, ErrorInvalidPath)
			if strings.Contains(err.Error(), invalid) {
				t.Errorf("error exposed invalid path: %v", err)
			}
		})
	}
	provider.logging.Access = ""
	result, err := reader.Read(context.Background(), Access, 3)
	if err != nil || result.Source != SourceJournal {
		t.Fatalf("empty configured path result = %#v, error = %v; want journal source", result, err)
	}
	provider.logging.Access = "none"
	result, err = reader.Read(context.Background(), Access, 3)
	if err != nil || result.Source != SourceNone || len(result.Lines) != 0 {
		t.Fatalf("disabled configured path result = %#v, error = %v; want empty none source", result, err)
	}
}

func TestReadJournalUsesFixedCommandForBothKinds(t *testing.T) {
	for _, kind := range []Kind{Access, ErrorLog} {
		t.Run(string(kind), func(t *testing.T) {
			fake := &fakeJournalRunner{output: []byte("earlier\r\n\r\nlater")}
			reader := readerForRoot(&fixedProvider{}, t.TempDir())
			reader.journalRunner = fake

			result, err := reader.Read(context.Background(), kind, 2)
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := []string{"-u", "xray.service", "-n", "2", "--no-pager", "-o", "short-iso"}
			if fake.executable != "/usr/bin/journalctl" || fmt.Sprint(fake.args) != fmt.Sprint(wantArgs) {
				t.Fatalf("journal command = %q %#v; want %q %#v",
					fake.executable, fake.args, "/usr/bin/journalctl", wantArgs)
			}
			if fake.calls != 1 {
				t.Fatalf("runner calls = %d; want 1", fake.calls)
			}
			if result.Kind != kind || result.Source != SourceJournal ||
				fmt.Sprint(result.Lines) != "[earlier later]" ||
				result.BytesRead != len("earlier\r\n\r\nlater") ||
				result.ByteLimit != MaxBytes || result.LineLimit != 2 || result.Truncated {
				t.Fatalf("journal result = %#v", result)
			}
		})
	}
}

func TestReadJournalLineParsingAndBounds(t *testing.T) {
	tests := []struct {
		name      string
		output    []byte
		limit     int
		want      []string
		truncated bool
	}{
		{name: "empty", output: []byte{}, limit: 2, want: []string{}},
		{name: "no newline", output: []byte("latest"), limit: 2, want: []string{"latest"}},
		{name: "CRLF and blank lines", output: []byte("first\r\n\r\nsecond\r\n"), limit: 2, want: []string{"first", "second"}},
		{name: "invalid UTF-8 is replaced", output: []byte{0xff, '\n', 'o', 'k'}, limit: 2, want: []string{"\uFFFD", "ok"}},
		{name: "line limit keeps newest in order", output: []byte("old\nmiddle\nlatest"), limit: 2, want: []string{"middle", "latest"}, truncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeJournalRunner{output: test.output}
			reader := readerForRoot(&fixedProvider{}, t.TempDir())
			reader.journalRunner = fake
			result, err := reader.Read(context.Background(), Access, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(result.Lines) != fmt.Sprint(test.want) || result.Truncated != test.truncated ||
				result.BytesRead != len(test.output) {
				t.Fatalf("result = %#v; want lines %q, truncated %t, bytes %d",
					result, test.want, test.truncated, len(test.output))
			}
		})
	}
}

func TestReadJournalMapsFailuresWithoutExposingOutputOrErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ErrorCode
	}{
		{"canceled", &commandrunner.Error{Failure: commandrunner.FailureCanceled}, ErrorCanceled},
		{"timeout", &commandrunner.Error{Failure: commandrunner.FailureTimeout}, ErrorDeadlineExceeded},
		{"unavailable", &commandrunner.Error{Failure: commandrunner.FailureStart}, ErrorJournalUnavailable},
		{"output limit", &commandrunner.Error{Failure: commandrunner.FailureOutputLimit}, ErrorOutputLimit},
		{"exit failure", &commandrunner.Error{Failure: commandrunner.FailureExit}, ErrorJournalFailed},
		{"unknown failure", errors.New("private-stderr /secret/argument"), ErrorJournalFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeJournalRunner{output: []byte("partial-private-output"), err: test.err}
			reader := readerForRoot(&fixedProvider{}, t.TempDir())
			reader.journalRunner = fake
			result, err := reader.Read(context.Background(), ErrorLog, 3)
			requireErrorCode(t, err, test.code)
			if len(result.Lines) != 0 || result.Source != "" {
				t.Fatalf("failed read returned partial result: %#v", result)
			}
			for _, secret := range []string{"private-stderr", "/secret/argument", "partial-private-output", "journalctl"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked %q: %v", secret, err)
				}
			}
			if test.code == ErrorDeadlineExceeded && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("timeout error = %v; want errors.Is(context.DeadlineExceeded)", err)
			}
			if test.code == ErrorCanceled && !errors.Is(err, context.Canceled) {
				t.Errorf("canceled error = %v; want errors.Is(context.Canceled)", err)
			}
		})
	}
}

func TestReadJournalRejectsOversizedRunnerOutput(t *testing.T) {
	fake := &fakeJournalRunner{output: []byte(strings.Repeat("x", MaxBytes+1))}
	reader := readerForRoot(&fixedProvider{}, t.TempDir())
	reader.journalRunner = fake

	result, err := reader.Read(context.Background(), Access, 1)
	requireErrorCode(t, err, ErrorOutputLimit)
	if len(result.Lines) != 0 || result.Source != "" {
		t.Fatalf("oversized journal read returned partial result: %#v", result)
	}
}

func TestReadJournalCancellationDuringCommandIsRedacted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeJournalRunner{
		output: []byte("partial-secret"),
		err:    errors.New("private stderr and argument"),
		onRun:  cancel,
	}
	reader := readerForRoot(&fixedProvider{}, t.TempDir())
	reader.journalRunner = fake

	result, err := reader.Read(ctx, Access, 1)
	requireErrorCode(t, err, ErrorCanceled)
	if len(result.Lines) != 0 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "partial-secret") {
		t.Fatalf("canceled journal read exposed data: result=%#v error=%v", result, err)
	}
}

func TestReadRejectsSymlinkTraversalAndNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.log")
	if err := os.WriteFile(secretPath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(root, "link.log")
	if err := os.Symlink(secretPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: symlinkPath}}, root)
	_, err := reader.Read(context.Background(), Access, 3)
	requireErrorCode(t, err, ErrorReadUnavailable)
	if strings.Contains(err.Error(), secretPath) {
		t.Errorf("symlink error exposed target path: %v", err)
	}

	linkDir := filepath.Join(root, "linked-dir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}
	reader = readerForRoot(&fixedProvider{
		logging: api.LoggingResponse{Access: filepath.Join(linkDir, "secret.log")},
	}, root)
	_, err = reader.Read(context.Background(), Access, 3)
	requireErrorCode(t, err, ErrorReadUnavailable)

	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	reader = readerForRoot(&fixedProvider{
		logging: api.LoggingResponse{Access: filepath.Join(rootLink, "link.log")},
	}, rootLink)
	_, err = reader.Read(context.Background(), Access, 3)
	requireErrorCode(t, err, ErrorReadUnavailable)

	dirPath := filepath.Join(root, "directory")
	if err := os.Mkdir(dirPath, 0700); err != nil {
		t.Fatal(err)
	}
	reader = readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: dirPath}}, root)
	_, err = reader.Read(context.Background(), Access, 3)
	requireErrorCode(t, err, ErrorNotRegular)
}

func TestReadUsesOpenedFileAcrossRotationReplacement(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "access.log")
	rotatedPath := filepath.Join(root, "access.log.1")
	content := strings.Repeat("old\n", MaxBytes/4) + "old-final"
	if err := os.WriteFile(logPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	base := noFollowFileOpener{}
	opener := rotatingOpener{
		base: base,
		rotate: func() {
			if err := os.Rename(logPath, rotatedPath); err != nil {
				t.Errorf("rotate log: %v", err)
			}
			if err := os.WriteFile(logPath, []byte("new-replacement\n"), 0600); err != nil {
				t.Errorf("create replacement: %v", err)
			}
		},
	}
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: logPath}}, root)
	reader.opener = opener
	result, err := reader.Read(context.Background(), Access, 1)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(result.Lines) != "[old-final]" {
		t.Fatalf("lines = %#v; opened descriptor should retain the rotated file", result.Lines)
	}
}

func TestReadCancellationAndRedactedFailures(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "access.log")
	if err := os.WriteFile(logPath, []byte("line"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: logPath}}, root)
	_, err := reader.Read(ctx, Access, 3)
	requireErrorCode(t, err, ErrorCanceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v; want errors.Is(context.Canceled)", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	provider := &fixedProvider{
		logging: api.LoggingResponse{Access: logPath},
		onCall:  cancel,
	}
	reader = readerForRoot(provider, root)
	_, err = reader.Read(ctx, Access, 3)
	requireErrorCode(t, err, ErrorCanceled)

	largePath := filepath.Join(root, "large.log")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", MaxBytes+100)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader = readerForRoot(&fixedProvider{logging: api.LoggingResponse{Access: largePath}}, root)
	reader.opener = cancelingOpener{base: noFollowFileOpener{}, cancel: cancel}
	_, err = reader.Read(ctx, Access, 3)
	requireErrorCode(t, err, ErrorCanceled)
	cancel()

	provider = &fixedProvider{err: errors.New("private-token /secret/path/config.json")}
	reader = readerForRoot(provider, root)
	_, err = reader.Read(context.Background(), Access, 3)
	requireErrorCode(t, err, ErrorPathUnavailable)
	for _, secret := range []string{"private-token", "/secret/path", "config.json"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("provider error exposed %q: %v", secret, err)
		}
	}
}

func TestReadNilContextReturnsRedactedErrorWithoutIO(t *testing.T) {
	provider := &fixedProvider{logging: api.LoggingResponse{Access: "/var/log/xray/access.log"}}
	opener := &countingOpener{}
	reader := &Reader{provider: provider, root: "/var/log/xray", opener: opener}

	_, err := reader.Read(nil, Access, 3)
	requireErrorCode(t, err, ErrorNilContext)
	if err.Error() != string(ErrorNilContext) {
		t.Fatalf("nil context error = %q; want redacted stable code", err)
	}
	if provider.calls != 0 {
		t.Errorf("provider calls = %d; want 0", provider.calls)
	}
	if opener.calls != 0 {
		t.Errorf("opener calls = %d; want 0", opener.calls)
	}
}

func readerForRoot(provider LoggingProvider, root string) *Reader {
	return &Reader{provider: provider, root: root, opener: noFollowFileOpener{}}
}

type fixedProvider struct {
	logging api.LoggingResponse
	err     error
	calls   int
	onCall  func()
}

func (provider *fixedProvider) Logging(context.Context) (api.LoggingResponse, error) {
	provider.calls++
	if provider.onCall != nil {
		provider.onCall()
	}
	return provider.logging, provider.err
}

type fakeJournalRunner struct {
	executable string
	args       []string
	output     []byte
	err        error
	calls      int
	onRun      func()
}

func (runner *fakeJournalRunner) Run(_ context.Context, executable string, args []string) ([]byte, error) {
	runner.calls++
	runner.executable = executable
	runner.args = append([]string(nil), args...)
	if runner.onRun != nil {
		runner.onRun()
	}
	return append([]byte(nil), runner.output...), runner.err
}

type countingOpener struct {
	calls int
}

func (opener *countingOpener) Open(string, string) (openedFile, error) {
	opener.calls++
	return nil, errors.New("unexpected file open")
}

func requireErrorCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("error = %v; want code %q", err, want)
	}
}

type rotatingOpener struct {
	base   fileOpener
	rotate func()
}

func (opener rotatingOpener) Open(root, path string) (openedFile, error) {
	file, err := opener.base.Open(root, path)
	if err != nil {
		return nil, err
	}
	return &rotatingFile{
		openedFile: file,
		once:       &sync.Once{},
		rotate:     opener.rotate,
	}, nil
}

type rotatingFile struct {
	openedFile
	once   *sync.Once
	rotate func()
}

func (file *rotatingFile) ReadAt(buffer []byte, offset int64) (int, error) {
	file.once.Do(file.rotate)
	return file.openedFile.ReadAt(buffer, offset)
}

type cancelingOpener struct {
	base   fileOpener
	cancel context.CancelFunc
}

func (opener cancelingOpener) Open(root, path string) (openedFile, error) {
	file, err := opener.base.Open(root, path)
	if err != nil {
		return nil, err
	}
	return &cancelingFile{openedFile: file, cancel: opener.cancel}, nil
}

type cancelingFile struct {
	openedFile
	cancel context.CancelFunc
}

func (file *cancelingFile) ReadAt(buffer []byte, offset int64) (int, error) {
	n, err := file.openedFile.ReadAt(buffer, offset)
	file.cancel()
	return n, err
}

func TestHelperJournalDoesNotDependOnXrayConfiguration(t *testing.T) {
	fake := &fakeJournalRunner{output: []byte("first\nlast\n")}
	reader := New(nil)
	reader.journalRunner = fake
	result, err := reader.Read(context.Background(), Helper, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-u", "xray-core-helper.service", "-n", "2", "--no-pager", "-o", "short-iso"}
	if fmt.Sprint(fake.args) != fmt.Sprint(want) || fake.executable != journalctlPath {
		t.Fatalf("unexpected command: %s %v", fake.executable, fake.args)
	}
	if result.Kind != Helper || result.Source != SourceJournal || fmt.Sprint(result.Lines) != "[first last]" {
		t.Fatalf("unexpected result: %#v", result)
	}
	_, err = reader.Read(context.Background(), Helper, MaxLines+1)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorInvalidLimit || fake.calls != 1 {
		t.Fatalf("invalid limit reached journal: %v, calls=%d", err, fake.calls)
	}
}
