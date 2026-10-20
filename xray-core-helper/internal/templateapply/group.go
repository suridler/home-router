package templateapply

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/nativewrite"
	"home-router/xray-core-helper/internal/templatecatalog"
)

// BaselineFile is private journal evidence, not a transport or path capability.
type BaselineFile struct {
	TemplateID templatecatalog.ID
	Present    bool
	Content    []byte
	Mode       uint32
	UID        uint32
	GID        uint32
}

func systemMember(id templatecatalog.ID) bool {
	return id == templatecatalog.Nftables || id == templatecatalog.Sysctl || id == templatecatalog.TProxyUnit
}

func (engine *Engine) groupPath(id templatecatalog.ID) (string, error) {
	if !systemMember(id) {
		return "", &Error{Code: CodeUnknownTemplate}
	}
	if engine.groupPaths != nil {
		path, ok := engine.groupPaths[id]
		if !ok {
			return "", &Error{Code: CodeRelatedRead}
		}
		return path, nil
	}
	entry, err := engine.catalog.Lookup(id)
	return entry.SystemPath, err
}

func captureBaselineFile(id templatecatalog.ID, path string) (BaselineFile, error) {
	file := BaselineFile{TemplateID: id, Content: []byte{}}
	if info, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return file, nil
	} else if err != nil {
		return file, &Error{Code: CodeRelatedRead}
	} else if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || (path == nativewrite.Target && info.Mode().Perm() == 0) {
		// Reject metadata the fixed recovery writer cannot reproduce before
		// any mutation, rather than discovering this during rollback.
		return file, &Error{Code: CodeRelatedRead}
	}
	raw, mode, uid, gid, _, err := readRegular(path)
	if err != nil || len(raw) > 1<<20 {
		return file, &Error{Code: CodeRelatedRead}
	}
	file.Present, file.Content, file.Mode, file.UID, file.GID = true, raw, uint32(mode.Perm()), uint32(uid), uint32(gid)
	return file, nil
}

// CaptureSystemBaseline captures all members before any system mutation.
func (engine *Engine) CaptureSystemBaseline(ctx context.Context) ([]BaselineFile, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	var files []BaselineFile
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := engine.groupPath(id)
		if err != nil {
			return nil, err
		}
		file, err := captureBaselineFile(id, path)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// RestoreSystemFile restores bytes/metadata or prior absence only. It executes
// no native commands; all runtime reconciliation waits for a manual reboot.
func (engine *Engine) RestoreSystemFile(ctx context.Context, file BaselineFile) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := engine.groupPath(file.TemplateID)
	if err != nil {
		return err
	}
	if len(file.Content) > 1<<20 || file.Mode & ^uint32(0777) != 0 {
		return &Error{Code: CodeRollback}
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &Error{Code: CodeRollback}
	}
	if err == nil && !info.Mode().IsRegular() {
		return &Error{Code: CodeRollback}
	}
	if !file.Present {
		if len(file.Content) != 0 || file.Mode != 0 || file.UID != 0 || file.GID != 0 {
			return &Error{Code: CodeRollback}
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if path == nativewrite.Target {
			return nativewrite.Remove(ctx)
		}
		if err = os.Remove(path); err != nil {
			return &Error{Code: CodeRollback}
		}
		return syncDirectory(filepath.Dir(path))
	}
	if err = atomicReplace(path, file.Content, os.FileMode(file.Mode), int(file.UID), int(file.GID)); err != nil {
		return &Error{Code: CodeRollback}
	}
	return nil
}

func (engine *Engine) VerifySystemBaseline(ctx context.Context, files []BaselineFile) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(files) != 3 {
		return &Error{Code: CodeRollback}
	}
	seen := map[templatecatalog.ID]bool{}
	for _, expected := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[expected.TemplateID] {
			return &Error{Code: CodeRollback}
		}
		seen[expected.TemplateID] = true
		path, err := engine.groupPath(expected.TemplateID)
		if err != nil {
			return err
		}
		actual, err := captureBaselineFile(expected.TemplateID, path)
		if err != nil || actual.Present != expected.Present || actual.Mode != expected.Mode || actual.UID != expected.UID || actual.GID != expected.GID || !bytes.Equal(actual.Content, expected.Content) {
			return &Error{Code: CodeRollback}
		}
	}
	return nil
}
