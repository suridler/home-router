// Package setup implements the transport-neutral fresh-host setup workflow.
package setup

import (
	"bytes"
	"errors"
	"os"
)

type ConfigState string

const (
	ConfigAbsent      ConfigState = "absent"
	ConfigEmpty       ConfigState = "empty"
	ConfigWhitespace  ConfigState = "whitespace"
	ConfigEmptyObject ConfigState = "empty_object"
	ConfigExisting    ConfigState = "existing"
	ConfigUnavailable ConfigState = "unavailable"
)

type ConfigProbe struct {
	Fingerprint      string      `json:"configFingerprint,omitempty"`
	State            ConfigState `json:"state"`
	SetupRequired    bool        `json:"setupRequired"`
	RequiresChoice   bool        `json:"requiresChoice"`
	RecoveryRequired bool        `json:"recoveryRequired"`
	Operation        *Operation  `json:"operation,omitempty"`
}

func ProbeConfig(path string) (ConfigProbe, error) {
	contents, _, err := readSafe(path)
	if errors.Is(err, os.ErrNotExist) {
		return ConfigProbe{State: ConfigAbsent, SetupRequired: true}, nil
	}
	if err != nil {
		return ConfigProbe{}, err
	}
	if len(contents) == 0 {
		return ConfigProbe{State: ConfigEmpty, SetupRequired: true}, nil
	}
	trimmed := bytes.TrimSpace(contents)
	if len(trimmed) == 0 {
		return ConfigProbe{State: ConfigWhitespace, SetupRequired: true}, nil
	}
	if bytes.Equal(trimmed, []byte("{}")) {
		return ConfigProbe{State: ConfigEmptyObject, SetupRequired: true}, nil
	}
	return ConfigProbe{State: ConfigExisting, Fingerprint: contentHash(contents), SetupRequired: true, RequiresChoice: true}, nil
}
