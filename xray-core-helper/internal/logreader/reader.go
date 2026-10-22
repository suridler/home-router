// Package logreader provides bounded, read-only access to Xray logs and the helper journal.
package logreader

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/commandrunner"
)

const (
	// MaxBytes is the largest amount of log-file data inspected per read.
	MaxBytes = 1 << 20
	// MaxLines is the largest permitted number of returned lines.
	MaxLines = 200

	xrayLogRoot    = "/var/log/xray"
	journalctlPath = "/usr/bin/journalctl"
	xrayUnit       = "xray.service"
	commandTimeout = 10 * time.Second
	readChunk      = 32 << 10
)

type Kind string

const (
	Access   Kind = "access"
	ErrorLog Kind = "error"
	Helper   Kind = "helper"
)

type Source string

const (
	SourceFile    Source = "file"
	SourceJournal Source = "journal"
	SourceNone    Source = "none"
)

type ErrorCode string

const (
	ErrorInvalidKind        ErrorCode = "invalid_kind"
	ErrorInvalidLimit       ErrorCode = "invalid_limit"
	ErrorInvalidPath        ErrorCode = "invalid_path"
	ErrorPathUnavailable    ErrorCode = "log_path_unavailable"
	ErrorNotRegular         ErrorCode = "log_not_regular"
	ErrorReadUnavailable    ErrorCode = "log_unavailable"
	ErrorJournalUnavailable ErrorCode = "journal_unavailable"
	ErrorJournalFailed      ErrorCode = "journal_failed"
	ErrorOutputLimit        ErrorCode = "output_limit"
	ErrorCanceled           ErrorCode = "canceled"
	ErrorDeadlineExceeded   ErrorCode = "deadline_exceeded"
	ErrorNilContext         ErrorCode = "nil_context"
)

// Error contains only a stable code; filesystem and configuration details are
// intentionally omitted.
type Error struct {
	Code  ErrorCode
	cause error
}

func (failure *Error) Error() string { return string(failure.Code) }

func (failure *Error) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

type Result struct {
	Kind      Kind
	Source    Source
	Lines     []string
	BytesRead int
	ByteLimit int
	LineLimit int
	Truncated bool
	Events    []Event
}

type LoggingProvider interface {
	Logging(context.Context) (api.LoggingResponse, error)
}

type openedFile interface {
	Stat() (os.FileInfo, error)
	ReadAt([]byte, int64) (int, error)
	Close() error
}

type fileOpener interface {
	Open(root, path string) (openedFile, error)
}

type commandRunner interface {
	Run(context.Context, string, []string) ([]byte, error)
}

type Reader struct {
	provider      LoggingProvider
	root          string
	opener        fileOpener
	journalRunner commandRunner
}

// New returns a reader restricted to the deployment's Xray log directory.
func New(provider LoggingProvider) *Reader {
	return &Reader{provider: provider, root: xrayLogRoot, opener: noFollowFileOpener{}}
}

func (reader *Reader) Read(ctx context.Context, kind Kind, lineLimit int) (Result, error) {
	return reader.read(ctx, kind, lineLimit, false, time.Time{})
}

// ReadHistory reads a bounded tail with stable identities for distinct occurrences.
func (reader *Reader) ReadHistory(ctx context.Context, since time.Time) (Result, error) {
	return reader.read(ctx, ErrorLog, 10000, true, since)
}

func (reader *Reader) read(ctx context.Context, kind Kind, lineLimit int, history bool, since time.Time) (Result, error) {
	if ctx == nil {
		return Result{}, &Error{Code: ErrorNilContext}
	}
	if kind != Access && kind != ErrorLog && kind != Helper {
		return Result{}, &Error{Code: ErrorInvalidKind}
	}
	if lineLimit < 1 || lineLimit > MaxLines && !history {
		return Result{}, &Error{Code: ErrorInvalidLimit}
	}
	if err := contextFailure(ctx.Err()); err != nil {
		return Result{}, err
	}
	if reader != nil && kind == Helper {
		return reader.readJournal(ctx, kind, lineLimit)
	}
	if reader == nil || reader.provider == nil || reader.opener == nil {
		return Result{}, &Error{Code: ErrorPathUnavailable}
	}

	logging, err := reader.provider.Logging(ctx)
	if err != nil {
		return Result{}, contextFailureOr(err, ErrorPathUnavailable)
	}
	if err := contextFailure(ctx.Err()); err != nil {
		return Result{}, err
	}

	configuredPath := logging.Access
	if kind == ErrorLog {
		configuredPath = logging.Error
	}
	if configuredPath == "" {
		if history {
			return reader.readJournalHistory(ctx, since)
		}
		return reader.readJournal(ctx, kind, lineLimit)
	}
	if configuredPath == "none" {
		return Result{
			Kind:      kind,
			Source:    SourceNone,
			Lines:     []string{},
			ByteLimit: MaxBytes,
			LineLimit: lineLimit,
		}, nil
	}
	if !cleanAbsolute(configuredPath) || !withinRoot(reader.root, configuredPath) {
		return Result{}, &Error{Code: ErrorInvalidPath}
	}

	file, err := reader.opener.Open(reader.root, configuredPath)
	if err != nil {
		if contextErr := contextFailure(ctx.Err()); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, contextFailureOr(err, ErrorReadUnavailable)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		if contextErr := contextFailure(ctx.Err()); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, contextFailureOr(err, ErrorReadUnavailable)
	}
	if err := contextFailure(ctx.Err()); err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() {
		return Result{}, &Error{Code: ErrorNotRegular}
	}
	if info.Size() < 0 {
		return Result{}, &Error{Code: ErrorReadUnavailable}
	}

	tail, bytesRead, truncated, err := readTail(ctx, file, info.Size())
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx.Err()); err != nil {
		return Result{}, err
	}
	var events []Event
	if history {
		events = fileEvents(tail, info, configuredPath)
	}
	lines := splitLines(tail)
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
		truncated = true
	}
	return Result{
		Kind:      kind,
		Source:    SourceFile,
		Events:    events,
		Lines:     lines,
		BytesRead: bytesRead,
		ByteLimit: MaxBytes,
		LineLimit: lineLimit,
		Truncated: truncated,
	}, nil
}

func (reader *Reader) readJournal(ctx context.Context, kind Kind, lineLimit int) (Result, error) {
	runner := reader.journalRunner
	if runner == nil {
		boundedRunner, err := commandrunner.NewWithOutputLimit(journalctlPath, commandTimeout, MaxBytes)
		if err != nil {
			return Result{}, &Error{Code: ErrorJournalUnavailable}
		}
		runner = boundedJournalRunner{runner: boundedRunner}
	}

	unit := xrayUnit
	if kind == Helper {
		unit = "xray-core-helper.service"
	}
	args := []string{"-u", unit, "-n", strconv.Itoa(lineLimit), "--no-pager", "-o", "short-iso"}
	output, err := runner.Run(ctx, journalctlPath, args)
	if contextErr := contextFailure(ctx.Err()); contextErr != nil {
		return Result{}, contextErr
	}
	if len(output) > MaxBytes {
		return Result{}, &Error{Code: ErrorOutputLimit}
	}
	if err != nil {
		return Result{}, journalFailure(ctx, err)
	}
	if err := contextFailure(ctx.Err()); err != nil {
		return Result{}, err
	}

	lines := splitLines([]byte(strings.ToValidUTF8(string(output), "\uFFFD")))
	truncated := false
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
		truncated = true
	}
	return Result{
		Kind:      kind,
		Source:    SourceJournal,
		Lines:     lines,
		BytesRead: len(output),
		ByteLimit: MaxBytes,
		LineLimit: lineLimit,
		Truncated: truncated,
	}, nil
}

type boundedJournalRunner struct {
	runner *commandrunner.Runner
}

func (runner boundedJournalRunner) Run(ctx context.Context, executable string, args []string) ([]byte, error) {
	if executable != journalctlPath {
		return nil, &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	result, err := runner.runner.Run(ctx, args)
	return result.Stdout(), err
}

func journalFailure(ctx context.Context, err error) error {
	if contextErr := contextFailure(ctx.Err()); contextErr != nil {
		return contextErr
	}
	var runnerErr *commandrunner.Error
	if errors.As(err, &runnerErr) {
		switch runnerErr.Failure {
		case commandrunner.FailureCanceled:
			return &Error{Code: ErrorCanceled, cause: context.Canceled}
		case commandrunner.FailureTimeout:
			return &Error{Code: ErrorDeadlineExceeded, cause: context.DeadlineExceeded}
		case commandrunner.FailureStart:
			return &Error{Code: ErrorJournalUnavailable}
		case commandrunner.FailureOutputLimit:
			return &Error{Code: ErrorOutputLimit}
		default:
			return &Error{Code: ErrorJournalFailed}
		}
	}
	return contextFailureOr(err, ErrorJournalFailed)
}

func readTail(ctx context.Context, file openedFile, fileSize int64) ([]byte, int, bool, error) {
	start := int64(0)
	tailBytes := int64(MaxBytes)
	if fileSize > MaxBytes {
		tailBytes--
		start = fileSize - tailBytes
	}
	remaining := fileSize - start
	if remaining > tailBytes {
		remaining = tailBytes
	}
	tail := make([]byte, int(remaining))
	read := 0
	for read < len(tail) {
		if err := contextFailure(ctx.Err()); err != nil {
			return nil, 0, false, err
		}
		end := read + readChunk
		if end > len(tail) {
			end = len(tail)
		}
		n, err := file.ReadAt(tail[read:end], start+int64(read))
		read += n
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, 0, false, contextFailureOr(err, ErrorReadUnavailable)
			}
			break
		}
		if n == 0 {
			break
		}
	}
	tail = tail[:read]
	bytesRead := read

	truncated := start > 0
	if start > 0 && len(tail) > 0 {
		if err := contextFailure(ctx.Err()); err != nil {
			return nil, 0, false, err
		}
		var previous [1]byte
		n, err := file.ReadAt(previous[:], start-1)
		if n == 1 {
			bytesRead++
		}
		if n == 1 && previous[0] != '\n' {
			if boundary := strings.IndexByte(string(tail), '\n'); boundary >= 0 {
				tail = tail[boundary+1:]
			} else {
				tail = nil
			}
		} else if err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, false, contextFailureOr(err, ErrorReadUnavailable)
		} else if n == 0 {
			if boundary := strings.IndexByte(string(tail), '\n'); boundary >= 0 {
				tail = tail[boundary+1:]
			} else {
				tail = nil
			}
		}
	}
	return tail, bytesRead, truncated, nil
}

func splitLines(tail []byte) []string {
	if len(tail) == 0 {
		return []string{}
	}
	rawLines := strings.Split(string(tail), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func withinRoot(root, path string) bool {
	if !cleanAbsolute(root) || root == string(filepath.Separator) {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func contextFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return &Error{Code: ErrorCanceled, cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrorDeadlineExceeded, cause: err}
	}
	return nil
}

func contextFailureOr(err error, fallback ErrorCode) error {
	if contextErr := contextFailure(err); contextErr != nil {
		return contextErr
	}
	return &Error{Code: fallback}
}

type noFollowFileOpener struct{}

func (noFollowFileOpener) Open(root, path string) (openedFile, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return nil, syscall.EPERM
	}

	directoryFD, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	openDirectory := func(parent int, name string) (int, error) {
		return syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	}
	closeDirectory := func() { _ = syscall.Close(directoryFD) }
	for _, component := range strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator)) {
		nextFD, openErr := openDirectory(directoryFD, component)
		closeDirectory()
		if openErr != nil {
			return nil, openErr
		}
		directoryFD = nextFD
	}

	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components[:len(components)-1] {
		nextFD, openErr := openDirectory(directoryFD, component)
		closeDirectory()
		if openErr != nil {
			return nil, openErr
		}
		directoryFD = nextFD
	}
	fd, openErr := syscall.Openat(directoryFD, components[len(components)-1],
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	closeDirectory()
	if openErr != nil {
		return nil, openErr
	}
	file := os.NewFile(uintptr(fd), "xray-log")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, os.ErrInvalid
	}
	return file, nil
}
