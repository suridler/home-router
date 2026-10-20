package routerdiagnostics

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"

	"home-router/xray-core-helper/internal/commandrunner"
)

const (
	maxDiagnosticLines = 4096
	maxDiagnosticLine  = 8192
)

func boundedLines(output []byte) ([]string, error) {
	if len(output) > commandrunner.MaxOutputBytes || !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return nil, errors.New("invalid diagnostic output")
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxDiagnosticLine)
	lines := make([]string, 0, 32)
	for scanner.Scan() {
		if len(lines) == maxDiagnosticLines {
			return nil, errors.New("diagnostic output limit")
		}
		lines = append(lines, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if scanner.Err() != nil {
		return nil, errors.New("invalid diagnostic line")
	}
	return lines, nil
}
func hasDefaultRoute(output []byte) (bool, error) {
	lines, err := boundedLines(output)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "default" || fields[0] == "0.0.0.0/0" {
			for i := 1; i+1 < len(fields); i++ {
				if fields[i] == "via" || fields[i] == "dev" {
					return true, nil
				}
			}
			return false, errors.New("invalid default route")
		}
	}
	return false, nil
}
func activeState(output []byte) (bool, error) {
	lines, err := boundedLines(output)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(strings.Join(lines, "\n")) {
	case "active":
		return true, nil
	case "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing":
		return false, nil
	default:
		return false, errors.New("invalid service status")
	}
}
