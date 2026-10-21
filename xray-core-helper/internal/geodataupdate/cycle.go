package geodataupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/usergeoip"
)

type CycleStore interface {
	Snapshot(context.Context) (domainstore.Snapshot, error)
	StageBaseline(context.Context, string, string, string, []string) error
	NeedsGeodataRestart(context.Context, string, string) (bool, error)
	ActivateBaselines(context.Context, string, string, map[string]bool) error
}

type CycleOptions struct {
	RoutingAssets func(context.Context) (map[string]bool, error)
	AssetDir      string
	Store         CycleStore
	Fetch         func(context.Context, bootstrap.GeodataConfig, string) ([]byte, error)
	Validate      func(context.Context, bootstrap.GeodataConfig, map[string]string) error
	Build         func(context.Context, bootstrap.GeodataConfig, string, []string) ([]byte, error)
	Replace       func(string, []byte) error
	Restart       func(context.Context) error
	Overlay       func(context.Context) error
	Identity      func(context.Context) (string, error)
}

type Cycle struct{ options CycleOptions }

func NewCycle(o CycleOptions) (*Cycle, error) {
	if !filepath.IsAbs(o.AssetDir) || o.Store == nil || o.Fetch == nil || o.Validate == nil || o.Restart == nil || o.Overlay == nil || o.Identity == nil || o.RoutingAssets == nil {
		return nil, errors.New("geodata cycle dependencies are required")
	}
	if o.Replace == nil {
		o.Replace = ReplaceAsset
	}
	if o.Build == nil {
		o.Build = BuildUserAsset
	}
	return &Cycle{options: o}, nil
}

// UpdateReport attempts both processes, independently validates each set, and
// restarts once. Network downloads deliberately precede the mutation lease.
func (c *Cycle) UpdateReport(ctx context.Context, cfg bootstrap.GeodataConfig) api.GeodataReport {
	return c.UpdateReportProgress(ctx, cfg, func(api.GeodataReport) error { return nil })
}

func (c *Cycle) UpdateReportProgress(ctx context.Context, cfg bootstrap.GeodataConfig, checkpoint func(api.GeodataReport) error) api.GeodataReport {
	report := api.GeodataReport{Files: []api.GeodataFileResult{}, Issues: []api.GeodataIssue{}}
	report.Process, report.Stage = "external", "download"
	if err := checkpoint(report); err != nil {
		c.issue(&report, "cycle", "", "save_state", err)
		return report
	}
	external := map[string][]byte{}
	for _, name := range []string{"geosite.dat", "geoip.dat"} {
		data, err := c.options.Fetch(ctx, cfg, name)
		if err != nil {
			c.issue(&report, "external", name, "download", err)
			continue
		}
		external[name] = data
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		c.issue(&report, "cycle", "", "lock", err)
		return report
	}
	defer lease.Release()
	token, _ := c.options.Identity(ctx)
	if token == "" {
		token = "unavailable"
	}
	c.process(ctx, cfg, &report, "external", external, nil, token, checkpoint)
	if report.Activation != nil {
		return report
	}
	snapshot, err := c.options.Store.Snapshot(ctx)
	if err != nil {
		c.issue(&report, "custom", "", "resources", err)
	} else {
		names, ips, err := domain.SplitResources(snapshot.Desired)
		if err != nil {
			c.issue(&report, "custom", "", "resources", err)
		} else {
			custom := map[string][]byte{}
			resources := map[string][]string{cfg.UserGeosite: names, cfg.UserGeoip: ips}
			for _, name := range []string{cfg.UserGeosite, cfg.UserGeoip} {
				data, err := c.options.Build(ctx, cfg, name, resources[name])
				if err != nil && !c.cleanupWarning(&report, "custom", err) {
					c.issue(&report, "custom", name, "build", err)
					continue
				}
				custom[name] = data
			}
			c.process(ctx, cfg, &report, "custom", custom, resources, token, checkpoint)
		}
	}
	if report.Activation != nil {
		return report
	}
	pendingRestart, err := c.options.Store.NeedsGeodataRestart(ctx, token, c.options.AssetDir)
	if err != nil {
		c.issue(&report, "cycle", "", "save_state", err)
		return report
	}
	if report.Replaced == 0 && !pendingRestart {
		return report
	}
	report.Process, report.Stage = "xray", "restart"
	if err := checkpoint(report); err != nil {
		c.issue(&report, "cycle", "", "save_state", err)
		return report
	}
	if err := c.options.Restart(ctx); err != nil {
		issue := safeIssue("xray", "", "restart", err)
		report.Activation = &issue
		return report
	}
	report.Restarted = true
	current, err := c.options.Identity(ctx)
	if err == nil && current == token {
		err = errors.New("Xray runtime identity did not change")
	}
	if err == nil {
		var assets map[string]bool
		assets, err = c.options.RoutingAssets(ctx)
		if err == nil {
			err = c.options.Store.ActivateBaselines(ctx, current, c.options.AssetDir, assets)
		}
	}
	if err != nil {
		issue := safeIssue("xray", "", "baseline", err)
		report.Activation = &issue
		return report
	}
	if err := c.options.Overlay(ctx); err != nil {
		issue := safeIssue("xray", "", "routing", err)
		report.Activation = &issue
	}
	return report
}

func (c *Cycle) process(ctx context.Context, cfg bootstrap.GeodataConfig, report *api.GeodataReport, process string, candidates map[string][]byte, resources map[string][]string, token string, checkpoint func(api.GeodataReport) error) {
	if len(candidates) == 0 {
		return
	}
	report.Process, report.Stage = process, "prepare"
	if err := checkpoint(*report); err != nil {
		c.issue(report, process, "", "save_state", err)
		return
	}
	dir, err := os.MkdirTemp(c.options.AssetDir, ".geodata-cycle-*")
	if err != nil {
		c.issue(report, process, "", "prepare", err)
		return
	}
	report.EvidenceDirectory = dir
	keepEvidence := false
	defer func() {
		if keepEvidence {
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			issue := safeIssue(process, "", "cleanup", err)
			issue.Warning = true
			report.Issues = append(report.Issues, issue)
		} else {
			report.EvidenceDirectory = ""
		}
	}()
	paths := map[string]string{}
	old := map[string][]byte{}
	absent := map[string]bool{}
	// Stable order makes failure reports reproducible.
	for _, name := range []string{"geosite.dat", "geoip.dat", cfg.UserGeosite, cfg.UserGeoip} {
		data, ok := candidates[name]
		if !ok {
			continue
		}
		if len(data) > geodatalimits.Bytes(cfg.MaxAssetSizeMB) {
			c.issue(report, process, name, "prepare", &geodatalimits.Exceeded{LimitBytes: geodatalimits.Bytes(cfg.MaxAssetSizeMB)})
			continue
		}
		previous, err := readAsset(filepath.Join(c.options.AssetDir, name), geodatalimits.Bytes(cfg.MaxAssetSizeMB))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			c.issue(report, process, name, "read", err)
			continue
		}
		if err == nil && bytes.Equal(previous, data) {
			report.Files = append(report.Files, api.GeodataFileResult{Process: process, File: name, State: "unchanged"})
			continue
		}
		old[name], absent[name] = previous, errors.Is(err, os.ErrNotExist)
		if !absent[name] {
			if err := os.WriteFile(filepath.Join(dir, name+".previous"), previous, 0600); err != nil {
				c.issue(report, process, name, "prepare", err)
				continue
			}
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			c.issue(report, process, name, "prepare", err)
			continue
		}
		paths[name] = path
	}
	if len(paths) == 0 {
		return
	}
	report.Process, report.Stage = process, "test"
	if err := checkpoint(*report); err != nil {
		c.issue(report, process, "", "save_state", err)
		return
	}
	if err := c.validate(ctx, cfg, report, process, paths); err != nil {
		c.issue(report, process, "", "test", err)
		return
	}
	report.Stage = "write"
	if err := checkpoint(*report); err != nil {
		c.issue(report, process, "", "save_state", err)
		return
	}
	changed := []string{}
	writeFailed := false
	for _, name := range []string{"geosite.dat", "geoip.dat", cfg.UserGeosite, cfg.UserGeoip} {
		if paths[name] == "" {
			continue
		}
		sum := sha256.Sum256(candidates[name])
		if err := c.options.Store.StageBaseline(ctx, name, hex.EncodeToString(sum[:]), token, resources[name]); err != nil {
			c.issue(report, process, name, "baseline_prepare", err)
			writeFailed = true
			continue
		}
		if err := c.options.Replace(filepath.Join(c.options.AssetDir, name), candidates[name]); err != nil {
			c.issue(report, process, name, "write", err)
			writeFailed = true
			actual, readErr := readAsset(filepath.Join(c.options.AssetDir, name), geodatalimits.Bytes(cfg.MaxAssetSizeMB))
			if readErr == nil && bytes.Equal(actual, candidates[name]) {
				changed = append(changed, name)
			}
			continue
		}
		changed = append(changed, name)
	}
	// A failed write changes the tested combination. Test the actual mixed set.
	if writeFailed && len(changed) > 0 {
		if err := c.validate(ctx, cfg, report, process, nil); err != nil {
			c.issue(report, process, "", "test_installed", err)

			for _, name := range changed {
				var err error
				if absent[name] {
					err = os.Remove(filepath.Join(c.options.AssetDir, name))
				} else {
					err = c.options.Replace(filepath.Join(c.options.AssetDir, name), old[name])
				}
				if err != nil {
					issue := safeIssue(process, name, "restore", err)
					report.Issues = append(report.Issues, issue)
					report.Activation = &issue
					keepEvidence = true
					report.Replaced++
				}
			}
			return
		}
	}
	for _, name := range changed {
		report.Replaced++
		report.Files = append(report.Files, api.GeodataFileResult{Process: process, File: name, State: "replaced"})
	}
}

func (c *Cycle) issue(report *api.GeodataReport, process, file, stage string, err error) {
	report.Issues = append(report.Issues, safeIssue(process, file, stage, err))
}

// Only known safe classifications cross the API/journal boundary. Raw command
// output and request URLs may contain secrets and are never copied here.
func safeIssue(process, file, stage string, err error) api.GeodataIssue {
	var assetFailure *geodatadownload.AssetFailure
	if errors.As(err, &assetFailure) {
		stage = assetFailure.Stage
	}
	var httpFailure *geodatadownload.HTTPStatusError
	var commandFailure *exec.ExitError
	message := "Операция не выполнена"
	var limit *geodatalimits.Exceeded
	switch {
	case errors.As(err, &httpFailure):
		message = fmt.Sprintf("Источник вернул HTTP %d", httpFailure.Status)
	case errors.As(err, &commandFailure):
		message = fmt.Sprintf("Программа завершилась с кодом %d", commandFailure.ExitCode())
	case errors.As(err, &limit):
		message = limit.Error()
	case errors.Is(err, geodatadownload.ErrDNS):
		message = "Не удалось определить адрес источника через DNS"
	case errors.Is(err, geodatadownload.ErrTLS):
		message = "Не удалось установить защищённое TLS-соединение с источником"
	case errors.Is(err, geodatadownload.ErrConnection):
		message = "Не удалось установить сетевое соединение с источником"
	case errors.Is(err, geodatadownload.ErrRedirect):
		message = "Источник вернул недопустимое перенаправление"
	case errors.Is(err, geodatadownload.ErrTruncatedResponse):
		message = "Получен неполный HTTP-ответ источника"
	case errors.Is(err, geodatadownload.ErrResponseTooLarge):
		message = "Файл контрольной суммы превышает лимит 4096 байт"
	case errors.Is(err, syscall.ENOSPC):
		message = "Недостаточно свободного места на диске"
	case errors.Is(err, syscall.EROFS):
		message = "Файловая система доступна только для чтения"
	case errors.Is(err, geodatadownload.ErrChecksumMismatch):
		message = "Контрольная сумма SHA256 не совпадает"
	case errors.Is(err, geodatadownload.ErrInvalidChecksum):
		message = "Некорректный файл контрольной суммы SHA256"
	case errors.Is(err, geodatadownload.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		message = "Истекло время ожидания"
	case errors.Is(err, geodatadownload.ErrInvalidConfiguration):
		message = "Источник отсутствует в geodata.origins или URL некорректен"
	case errors.Is(err, geodatadownload.ErrSourcesDisabled):
		message = "URL базы или контрольной суммы не задан"
	case errors.Is(err, os.ErrPermission):
		message = "Недостаточно прав для чтения или записи файла"
	case errors.Is(err, os.ErrNotExist):
		message = "Не найден необходимый файл или исполняемая программа"
	case errors.Is(err, context.Canceled):
		message = "Операция прервана"
	default:
		switch stage {
		case "test", "test_installed":
			message = "xray run -test отклонил набор геобаз"
		case "download":
			message = "Не удалось скачать базу или её контрольную сумму"
		case "build":
			message = "Не удалось собрать пользовательскую базу"
		case "restart":
			message = "Базы заменены; перезапуск Xray завершился ошибкой"
		case "routing":
			message = "Xray перезапущен; динамические правила не восстановлены"
		case "baseline", "baseline_prepare":
			message = "Не удалось сохранить состояние пользовательских ресурсов"
		case "cleanup":
			message = "Не удалось удалить временные файлы"
		case "save_state":
			message = "Не удалось сохранить результат операции"
		case "write", "prepare":
			message = "Не удалось записать файл базы"
		case "read":
			message = "Не удалось прочитать действующий файл базы"
		case "resources":
			message = "Не удалось прочитать список пользовательских ресурсов"
		case "restore":
			message = "Критическая ошибка: не удалось восстановить файл после failed test"
		}
	}
	code := "geodata_" + process + "_" + stage + "_failed"
	if errors.As(err, &limit) {
		code = "geodata_asset_too_large"
	}
	limitMB := 0
	if limit != nil {
		limitMB = limit.LimitBytes / geodatalimits.BytesPerMB
	}
	return api.GeodataIssue{Process: process, File: file, Stage: stage, Code: code, Message: message, At: time.Now().UnixMilli(), MaxAssetSizeMB: limitMB}
}

func readAsset(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(data) > limit {
		return nil, &geodatalimits.Exceeded{LimitBytes: limit}
	}
	return data, err
}

// ReplaceAsset never truncates the live file, and preserves administrator modes.
func ReplaceAsset(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".geodata-replace-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if info, statErr := os.Stat(path); statErr == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := f.Chown(int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
		if err := f.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else if err := f.Chmod(0644); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func BuildUserAsset(ctx context.Context, cfg bootstrap.GeodataConfig, name string, resources []string) (data []byte, err error) {
	if name == cfg.UserGeoip {
		return usergeoip.Encode(resources)
	}
	if name != cfg.UserGeosite {
		return nil, errors.New("unknown custom asset")
	}
	dir, err := os.MkdirTemp("", "xray-user-geosite-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil && err == nil {
			err = &cleanupWarning{}
		}
	}()
	var input strings.Builder
	if len(resources) == 0 {
		input.WriteString("domain:empty.userlist.invalid\n")
	}
	for _, name := range resources {
		fmt.Fprintf(&input, "domain:%s\n", name)
	}
	if err := os.WriteFile(filepath.Join(dir, "userlist"), []byte(input.String()), 0600); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, cfg.Builder, "-datapath", dir, "-outputdir", dir, "-outputname", name)
	// Builder output is not safe to expose: resource lists may be private.
	if err := command.Run(); err != nil {
		return nil, err
	}
	return readAsset(filepath.Join(dir, name), geodatalimits.Bytes(cfg.MaxAssetSizeMB))
}

type cleanupWarning struct{}

func (*cleanupWarning) Error() string { return "temporary data cleanup failed" }
func (*cleanupWarning) Warning() bool { return true }

func (c *Cycle) cleanupWarning(report *api.GeodataReport, process string, err error) bool {
	var warning interface{ Warning() bool }
	if !errors.As(err, &warning) || !warning.Warning() {
		return false
	}
	issue := safeIssue(process, "", "cleanup", err)
	issue.Warning = true
	issue.Message = "Не удалось удалить временные копии; результат обработки сохранён"
	report.Issues = append(report.Issues, issue)
	return true
}

func (c *Cycle) validate(ctx context.Context, cfg bootstrap.GeodataConfig, report *api.GeodataReport, process string, paths map[string]string) error {
	err := c.options.Validate(ctx, cfg, paths)
	if c.cleanupWarning(report, process, err) {
		return nil
	}
	return err
}
