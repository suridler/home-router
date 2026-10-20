package routerdiagnostics

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/bootstrap"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayidentity"
)

func readNative(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("native file unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("native file unavailable")
	}
	return raw, nil
}

func declaredChecks(ctx context.Context, xrayPath string, network bootstrap.NetworkConfig) ([]nativebinding.Check, error) {
	documents := map[string]*nativebinding.Document{}
	for _, entry := range templatecatalog.Default().Entries() {
		if entry.ID == templatecatalog.XrayConfig {
			continue
		}
		raw, err := readNative(entry.SystemPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nativebinding.ErrUnresolved
		}
		doc, err := nativebinding.Parse(raw)
		if err != nil {
			return nil, err
		}
		documents[string(entry.ID)] = doc
	}
	return nativebinding.CheckDocuments(documents, func(source string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.HasPrefix(source, "helper.network.") {
			return network.BindingValue(source)
		}
		if source == "xray.service-gid" {
			gid, err := xrayidentity.New().GID(ctx)
			return strconv.Itoa(gid), err
		}
		raw, err := readNative(xrayPath)
		if err != nil {
			return "", nativebinding.ErrUnresolved
		}
		return xrayconfig.BindingValue(raw, source)
	})
}
