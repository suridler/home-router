package httpapi

import (
	"context"
	"encoding/json"
	"home-router/xray-core-helper/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type backupFake struct {
	records    []templatebackup.Record
	generation uint64
	restoreErr error
}

func (fake *backupFake) List(context.Context, templatecatalog.ID) ([]templatebackup.Record, uint64, error) {
	return fake.records, fake.generation, nil
}
func (fake *backupFake) Restore(context.Context, templatecatalog.ID, string, uint64) (templatebackup.Record, error) {
	if fake.restoreErr != nil {
		return templatebackup.Record{}, fake.restoreErr
	}
	fake.generation++
	return fake.records[0], nil
}

func TestTemplateBackupListAndRestoreAreTypedAndRedacted(t *testing.T) {
	record := templatebackup.Record{BackupID: "20261014-1", TemplateID: "xray-config", Side: templatebackup.SideSystem, Hash: strings.Repeat("a", 64), Mode: 0600, UID: 0, GID: 0, Generation: 1, State: templatebackup.StateCompleted, CreatedAt: time.Unix(1, 0).UTC()}
	fake := &backupFake{records: []templatebackup.Record{record}, generation: 1}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Backups: fake}).Handler
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/templates/xray-config/backups", nil))
	if list.Code != 200 {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	for _, forbidden := range []string{"config.json", "privateKey", "secret-value"} {
		if strings.Contains(list.Body.String(), forbidden) {
			t.Fatalf("leak %q in %s", forbidden, list.Body.String())
		}
	}
	restore := httptest.NewRecorder()
	handler.ServeHTTP(restore, httptest.NewRequest(http.MethodPost, "/v1/templates/xray-config/backups/20261014-1/restore", strings.NewReader(`{"expectedGeneration":1}`)))
	if restore.Code != 200 || !strings.Contains(restore.Body.String(), `"generation":2`) {
		t.Fatalf("restore=%d %s", restore.Code, restore.Body.String())
	}
}

func TestTemplateBackupRestoreErrorsAreStable(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{{templatebackup.ErrConflict, 409, "backup_generation_conflict"}, {templatebackup.ErrValidation, 422, "backup_validation_failed"}, {templatebackup.ErrNotFound, 404, "backup_not_found"}, {templatebackup.ErrFilesystem, 503, "template_backups_unavailable"}} {
		fake := &backupFake{records: []templatebackup.Record{{BackupID: "one"}}, generation: 1, restoreErr: test.err}
		handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Backups: fake}).Handler
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/xray-config/backups/one/restore", strings.NewReader(`{"expectedGeneration":1}`)))
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Errorf("err=%v response=%d %s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestBackupListSeparatesCompletedGenerationAfterRollback(t *testing.T) {
	for _, test := range []struct {
		name                  string
		records               []templatebackup.Record
		generation, completed uint64
	}{
		{"empty", nil, 0, 0},
		{"rollback only", []templatebackup.Record{{Generation: 2, State: templatebackup.StateRollback}}, 2, 0},
		{"completed then rollback", []templatebackup.Record{{Generation: 2, State: templatebackup.StateRollback}, {Generation: 1, State: templatebackup.StateCompleted}}, 2, 1},
		{"pending after completed", []templatebackup.Record{{Generation: 1, State: templatebackup.StateCompleted}, {Generation: 3, State: templatebackup.StatePending}}, 3, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &backupFake{records: test.records, generation: test.generation}
			response := httptest.NewRecorder()
			listTemplateBackups(response, httptest.NewRequest(http.MethodGet, "/", nil), fake, templatecatalog.Sysctl)
			var result api.TemplateBackupsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || result.Generation != test.generation || result.CompletedGeneration != test.completed || len(result.Backups) != len(test.records) {
				t.Fatalf("response=%d %+v", response.Code, result)
			}
		})
	}
}
