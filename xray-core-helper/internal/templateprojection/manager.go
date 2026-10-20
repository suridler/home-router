// Package templateprojection owns the last complete read-only template status
// snapshot and synchronization preconditions for later mutations.
package templateprojection

import (
	"context"
	"errors"
	"sync"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type Refresher interface {
	Refresh(context.Context) api.TemplateStatusResponse
}

type Code string

const (
	CodeStatusUnavailable       Code = "template_status_unavailable"
	CodeUnknownTemplate         Code = "unknown_template"
	CodeSynchronizationRequired Code = "template_synchronization_required"
	CodeRevisionConflict        Code = "template_revision_conflict"
)

type Error struct{ Code Code }

func (failure *Error) Error() string { return string(failure.Code) }

type Manager struct {
	mu        sync.RWMutex
	refresher Refresher
	current   api.TemplateStatusResponse
	ready     bool
}

func New(refresher Refresher) (*Manager, error) {
	if refresher == nil {
		return nil, &Error{Code: CodeStatusUnavailable}
	}
	return &Manager{refresher: refresher}, nil
}

// Initialize performs the startup read-only refresh.
func (manager *Manager) Initialize(ctx context.Context) api.TemplateStatusResponse {
	return manager.Refresh(ctx)
}

// Refresh atomically replaces the projection only after all rows are collected.
func (manager *Manager) Refresh(ctx context.Context) api.TemplateStatusResponse {
	next := clone(manager.refresher.Refresh(ctx))
	manager.mu.Lock()
	manager.current = next
	manager.ready = true
	manager.mu.Unlock()
	return clone(next)
}

func (manager *Manager) Current() (api.TemplateStatusResponse, bool) {
	if manager == nil {
		return api.TemplateStatusResponse{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.ready {
		return api.TemplateStatusResponse{}, false
	}
	return clone(manager.current), true
}

// RequireSynchronized guards ordinary mutations of one mapped system file.
// Directional synchronization operations use their own expectedRevision check.
func (manager *Manager) RequireSynchronized(id templatecatalog.ID, expectedRevision string) error {
	if _, err := templatecatalog.Default().Lookup(id); err != nil {
		return &Error{Code: CodeUnknownTemplate}
	}
	current, ok := manager.Current()
	if !ok {
		return &Error{Code: CodeStatusUnavailable}
	}
	for _, status := range current.Templates {
		if status.TemplateID != string(id) {
			continue
		}
		if status.Revision != expectedRevision {
			return &Error{Code: CodeRevisionConflict}
		}
		if status.State != api.TemplateSynchronized {
			return &Error{Code: CodeSynchronizationRequired}
		}
		return nil
	}
	return &Error{Code: CodeStatusUnavailable}
}

func clone(source api.TemplateStatusResponse) api.TemplateStatusResponse {
	return api.TemplateStatusResponse{Templates: append([]api.TemplateStatus(nil), source.Templates...)}
}

func IsCode(err error, code Code) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Code == code
}
