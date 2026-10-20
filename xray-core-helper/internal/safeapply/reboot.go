package safeapply

import (
	"errors"
	"os"
	"strings"

	"home-router/xray-core-helper/internal/templatecatalog"
)

var ErrRebootRequired = errors.New("manual reboot required; system configuration files restored, runtime effects not restored")
var ErrBootUnavailable = errors.New("boot_identity_unavailable")

func currentBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", ErrBootUnavailable
	}
	id := strings.TrimSpace(string(raw))
	if !validBootID(id) {
		return "", ErrBootUnavailable
	}
	return id, nil
}

func validBootID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (service *Service) manualReboot(id templatecatalog.ID) bool {
	engine, ok := service.engine.(interface{ RollbackNeedsReboot(templatecatalog.ID) bool })
	return ok && engine.RollbackNeedsReboot(id)
}
