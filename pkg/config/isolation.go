// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"fmt"
	"strings"
)

const (
	maxIsolationEnvironmentAllowlistNames = 128
	maxIsolationEnvironmentNameBytes      = 128
)

var defaultIsolationEnvironmentAllowlist = [...]string{
	"PATH",
	"HOME",
	"TMPDIR",
	"XDG_CONFIG_HOME",
	"XDG_CACHE_HOME",
	"XDG_STATE_HOME",
	"PATHEXT",
	"USERPROFILE",
	"HOMEDRIVE",
	"HOMEPATH",
	"TEMP",
	"TMP",
	"APPDATA",
	"LOCALAPPDATA",
	"LANG",
	"LANGUAGE",
	"LC_ALL",
	"LC_CTYPE",
	"LC_COLLATE",
	"LC_MESSAGES",
	"LC_MONETARY",
	"LC_NUMERIC",
	"LC_TIME",
	"TZ",
	"TERM",
	"COLORTERM",
	"NO_COLOR",
}

// DefaultIsolationEnvironmentAllowlist returns the portable compatibility
// allowlist used when the configuration omits environment_allowlist or a
// programmatic caller supplies nil. Each call returns detached storage.
func DefaultIsolationEnvironmentAllowlist() []string {
	names := make([]string, len(defaultIsolationEnvironmentAllowlist))
	copy(names, defaultIsolationEnvironmentAllowlist[:])
	return names
}

// ValidateEnvironmentAllowlist validates portable environment variable names.
// Duplicate detection is case-insensitive on every platform so a configuration
// that is valid on Unix cannot become ambiguous when moved to Windows.
func (c IsolationConfig) ValidateEnvironmentAllowlist() error {
	if len(c.EnvironmentAllowlist) > maxIsolationEnvironmentAllowlistNames {
		return fmt.Errorf(
			"environment_allowlist has %d entries; maximum is %d",
			len(c.EnvironmentAllowlist),
			maxIsolationEnvironmentAllowlistNames,
		)
	}

	for i, name := range c.EnvironmentAllowlist {
		if !validIsolationEnvironmentName(name) {
			return fmt.Errorf(
				"environment_allowlist[%d] must be a valid ASCII environment variable name no longer than %d bytes",
				i,
				maxIsolationEnvironmentNameBytes,
			)
		}
		for previous := 0; previous < i; previous++ {
			if strings.EqualFold(c.EnvironmentAllowlist[previous], name) {
				return fmt.Errorf(
					"environment_allowlist[%d] duplicates environment_allowlist[%d] case-insensitively",
					i,
					previous,
				)
			}
		}
	}
	return nil
}

func validIsolationEnvironmentName(name string) bool {
	if name == "" || len(name) > maxIsolationEnvironmentNameBytes ||
		!isIsolationEnvironmentNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		character := name[i]
		if !isIsolationEnvironmentNameStart(character) &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func isIsolationEnvironmentNameStart(character byte) bool {
	return character == '_' ||
		character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z'
}
