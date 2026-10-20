// Package templatecompare safely compares fixed working templates with their
// mapped native system files.
package templatecompare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const MaxFileBytes int64 = 16 << 20

type Code string

const (
	CodeUnsafeFile  Code = "unsafe_file"
	CodeTooLarge    Code = "content_too_large"
	CodeUnreadable  Code = "read_failed"
	CodeChanged     Code = "file_changed"
	CodeBothMissing Code = "both_missing"
	CodeBlock       Code = "invalid_block_markers"
)

type readError struct{ code Code }

func (failure *readError) Error() string { return string(failure.code) }

type Snapshot struct {
	Present bool
	Hash    string
}

type Reader interface {
	Read(context.Context, string) (Snapshot, error)
}

type SafeReader struct {
	maxBytes  int64
	afterRead func(string)
}

func NewSafeReader(maxBytes int64) (*SafeReader, error) {
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return nil, &readError{code: CodeTooLarge}
	}
	return &SafeReader{maxBytes: maxBytes}, nil
}

func (reader *SafeReader) Read(ctx context.Context, path string) (Snapshot, error) {
	if reader == nil || ctx == nil || reader.maxBytes < 1 {
		return Snapshot{}, &readError{code: CodeUnreadable}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var initial unix.Stat_t
	if err := unix.Lstat(path, &initial); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return Snapshot{}, nil
		}
		return Snapshot{}, &readError{code: CodeUnreadable}
	}
	present := Snapshot{Present: true}
	if initial.Mode&unix.S_IFMT != unix.S_IFREG {
		return present, &readError{code: CodeUnsafeFile}
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return present, &readError{code: CodeChanged}
		}
		if errors.Is(err, unix.ELOOP) {
			return present, &readError{code: CodeUnsafeFile}
		}
		return present, &readError{code: CodeUnreadable}
	}
	file := os.NewFile(uintptr(fd), "managed-template")
	if file == nil {
		_ = unix.Close(fd)
		return present, &readError{code: CodeUnreadable}
	}
	defer file.Close()

	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG {
		return present, &readError{code: CodeUnsafeFile}
	}
	if !sameIdentity(initial, before) {
		return present, &readError{code: CodeChanged}
	}
	if before.Size < 0 || before.Size > reader.maxBytes {
		return present, &readError{code: CodeTooLarge}
	}

	hash := sha256.New()
	limited := io.LimitReader(file, reader.maxBytes+1)
	written, err := copyWithContext(ctx, hash, limited)
	if err != nil {
		return present, err
	}
	if written > reader.maxBytes {
		return present, &readError{code: CodeTooLarge}
	}
	if reader.afterRead != nil {
		reader.afterRead(path)
	}

	var after, current unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || unix.Lstat(path, &current) != nil {
		return present, &readError{code: CodeChanged}
	}
	if !sameSnapshot(before, after) || !sameIdentity(after, current) || after.Size != written {
		return present, &readError{code: CodeChanged}
	}
	present.Hash = hex.EncodeToString(hash.Sum(nil))
	return present, nil
}

type Comparer struct {
	reader      Reader
	userGeosite string
	render      func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)
}

func New(reader Reader, userGeosite ...string) (*Comparer, error) {
	if reader == nil {
		var err error
		reader, err = NewSafeReader(MaxFileBytes)
		if err != nil {
			return nil, err
		}
	}
	name := ""
	if len(userGeosite) > 0 {
		name = userGeosite[0]
	}
	return &Comparer{reader: reader, userGeosite: name}, nil
}

// SetRenderer configures read-only candidate rendering for status comparison.
func (comparer *Comparer) SetRenderer(render func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)) {
	comparer.render = render
}

func (comparer *Comparer) Compare(ctx context.Context, entry templatecatalog.Entry) api.TemplateStatus {
	template, templateErr := comparer.reader.Read(ctx, entry.WorkingPath)
	system, systemErr := comparer.reader.Read(ctx, entry.SystemPath)
	fullTemplateHash, fullSystemHash := template.Hash, system.Hash
	if comparer.render == nil && comparer.userGeosite != "" && entry.ID == templatecatalog.XrayConfig && templateErr == nil && template.Present {
		if reader, ok := comparer.reader.(*SafeReader); ok {
			var bound []byte
			bound, templateErr = reader.BoundXray(ctx, entry.WorkingPath, comparer.userGeosite)
			if templateErr == nil {
				snapshot, err := reader.Read(ctx, entry.WorkingPath)
				if err != nil || snapshot.Hash != fullTemplateHash {
					templateErr = &readError{code: CodeChanged}
				} else {
					sum := sha256.Sum256(bound)
					template.Hash = hex.EncodeToString(sum[:])
				}
			}
		}
	}
	var rendered []byte
	if comparer.render != nil && templateErr == nil && template.Present {
		if reader, ok := comparer.reader.(*SafeReader); ok {
			var raw []byte
			raw, templateErr = reader.ReadContent(ctx, entry.WorkingPath)
			if templateErr == nil {
				sum := sha256.Sum256(raw)
				if hex.EncodeToString(sum[:]) != fullTemplateHash {
					templateErr = &readError{code: CodeChanged}
				} else {
					rendered, templateErr = comparer.render(ctx, entry, raw)
				}
			}
			if templateErr == nil {
				sum := sha256.Sum256(rendered)
				template.Hash = hex.EncodeToString(sum[:])
			}
		}
	}
	initializationRequired := false
	if reader, ok := comparer.reader.(*SafeReader); ok && entry.ID == templatecatalog.Nftables {
		if templateErr == nil && template.Present {
			if rendered != nil {
				var block []byte
				block, templateErr = nativeblock.Content(rendered)
				if templateErr == nil {
					sum := sha256.Sum256(block)
					template.Hash = hex.EncodeToString(sum[:])
				} else {
					templateErr = &readError{code: CodeBlock}
				}
			} else {
				template.Hash, templateErr = reader.blockHash(ctx, entry.WorkingPath, template.Hash)
			}
		}
		if systemErr == nil && system.Present {
			var hash string
			hash, systemErr = reader.blockHash(ctx, entry.SystemPath, system.Hash)
			if errors.Is(systemErr, nativeblock.ErrInitialize) {
				initializationRequired = true
				sum := sha256.Sum256([]byte("unmarked\x00" + fullSystemHash))
				hash, systemErr = hex.EncodeToString(sum[:]), nil
			}
			system.Hash = hash
		}
	}
	status := api.TemplateStatus{
		TemplateID:   string(entry.ID),
		TemplateHash: template.Hash,
		SystemHash:   system.Hash,
		Summary: api.TemplateStatusSummary{
			BlockInitializationRequired: initializationRequired,
			TemplateAvailable:           template.Present,
			SystemAvailable:             system.Present,
		},
	}

	switch {
	case templateErr != nil || systemErr != nil:
		status.State = api.TemplateInvalid
		status.Summary.InvalidSide = invalidSide(templateErr, systemErr)
		status.Summary.Code = combinedCode(templateErr, systemErr)
	case !template.Present && !system.Present:
		status.State = api.TemplateInvalid
		status.Summary.InvalidSide = api.TemplateSideBoth
		status.Summary.Code = string(CodeBothMissing)
	case !template.Present:
		status.State = api.TemplateMissing
	case !system.Present:
		status.State = api.TemplateSystemMissing
	case template.Hash == system.Hash:
		status.State = api.TemplateSynchronized
	default:
		status.State = api.TemplateDifferent
	}
	guard := status
	guard.TemplateHash, guard.SystemHash = fullTemplateHash, fullSystemHash
	if comparer.userGeosite != "" && entry.ID == templatecatalog.XrayConfig || comparer.render != nil {
		guard.TemplateHash += "\x00" + template.Hash
	}
	status.Revision = revision(guard)
	return status
}

// BoundXray reads a stable no-follow snapshot and renders only declared helper
// bindings for comparison/validation, without changing the working template.
func (reader *SafeReader) BoundXray(ctx context.Context, path, name string) ([]byte, error) {
	raw, err := reader.ReadContent(ctx, path)
	if err != nil {
		return nil, err
	}
	return xrayconfig.BindUserGeosite(raw, name)
}

// ReadContent reads a bounded stable regular file without following symlinks.
func (reader *SafeReader) ReadContent(ctx context.Context, path string) ([]byte, error) {
	before, err := reader.Read(ctx, path)
	if err != nil || !before.Present {
		return nil, &readError{code: CodeUnreadable}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &readError{code: CodeUnreadable}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, &readError{code: CodeUnsafeFile}
	}
	raw, err := io.ReadAll(io.LimitReader(file, reader.maxBytes+1))
	if err != nil || int64(len(raw)) > reader.maxBytes {
		return nil, &readError{code: CodeTooLarge}
	}
	sum := sha256.Sum256(raw)
	after, err := reader.Read(ctx, path)
	if err != nil || before.Hash != after.Hash || before.Hash != hex.EncodeToString(sum[:]) {
		return nil, &readError{code: CodeChanged}
	}
	return raw, nil
}

func (reader *SafeReader) blockHash(ctx context.Context, path, expected string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", &readError{code: CodeUnreadable}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", &readError{code: CodeUnsafeFile}
	}
	raw, err := io.ReadAll(io.LimitReader(file, nativeblock.MaxBytes+1))
	if err != nil {
		return "", &readError{code: CodeUnreadable}
	}
	if len(raw) > nativeblock.MaxBytes {
		return "", &readError{code: CodeTooLarge}
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expected {
		return "", &readError{code: CodeChanged}
	}
	after, err := reader.Read(ctx, path)
	if err != nil || after.Hash != expected {
		return "", &readError{code: CodeChanged}
	}
	block, err := nativeblock.Content(raw)
	if errors.Is(err, nativeblock.ErrInitialize) {
		return "", err
	}
	if err != nil {
		return "", &readError{code: CodeBlock}
	}
	sum = sha256.Sum256(block)
	return hex.EncodeToString(sum[:]), nil
}

func revision(status api.TemplateStatus) string {
	fields := []string{
		"v1", status.TemplateID,
		strconv.FormatBool(status.Summary.TemplateAvailable), status.TemplateHash,
		strconv.FormatBool(status.Summary.SystemAvailable), status.SystemHash,
	}
	hash := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(hash[:])
}

func invalidSide(templateErr, systemErr error) api.TemplateSide {
	if templateErr != nil && systemErr != nil {
		return api.TemplateSideBoth
	}
	if templateErr != nil {
		return api.TemplateSideTemplate
	}
	return api.TemplateSideSystem
}

func combinedCode(templateErr, systemErr error) string {
	if templateErr != nil && systemErr != nil {
		return "multiple_read_failures"
	}
	if templateErr != nil {
		return errorCode(templateErr)
	}
	return errorCode(systemErr)
}

func errorCode(err error) string {
	if errors.Is(err, nativebinding.ErrUnresolved) {
		return "binding_unresolved"
	}
	if errors.Is(err, nativebinding.ErrInvalid) || errors.Is(err, nativebinding.ErrLimit) {
		return "binding_invalid"
	}
	var failure *readError
	if errors.As(err, &failure) {
		return string(failure.code)
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, nativeblock.ErrInitialize) {
		return string(CodeBlock)
	}
	return string(CodeUnreadable)
}

func sameIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode&unix.S_IFMT == right.Mode&unix.S_IFMT
}

func sameSnapshot(left, right unix.Stat_t) bool {
	return sameIdentity(left, right) && left.Size == right.Size &&
		left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil || written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
