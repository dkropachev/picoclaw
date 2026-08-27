// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var expectedDefaultIsolationEnvironmentAllowlist = []string{
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

func TestDefaultIsolationEnvironmentAllowlistExactAndDetached(t *testing.T) {
	first := DefaultIsolationEnvironmentAllowlist()
	if !reflect.DeepEqual(first, expectedDefaultIsolationEnvironmentAllowlist) {
		t.Fatalf(
			"DefaultIsolationEnvironmentAllowlist() = %#v, want %#v",
			first,
			expectedDefaultIsolationEnvironmentAllowlist,
		)
	}
	if cap(first) != len(first) {
		t.Fatalf("default allowlist len/cap = %d/%d, want equal", len(first), cap(first))
	}

	first[0] = "MUTATED"
	second := DefaultIsolationEnvironmentAllowlist()
	if !reflect.DeepEqual(second, expectedDefaultIsolationEnvironmentAllowlist) {
		t.Fatalf("fresh default allowlist retained caller mutation: %#v", second)
	}

	firstConfig := DefaultConfig()
	secondConfig := DefaultConfig()
	firstConfig.Isolation.EnvironmentAllowlist[0] = "CONFIG_MUTATION"
	if !reflect.DeepEqual(
		secondConfig.Isolation.EnvironmentAllowlist,
		expectedDefaultIsolationEnvironmentAllowlist,
	) {
		t.Fatalf(
			"DefaultConfig isolation allowlists share storage: %#v",
			secondConfig.Isolation.EnvironmentAllowlist,
		)
	}
}

func TestLoadConfigIsolationEnvironmentAllowlistPersistenceSemantics(t *testing.T) {
	mustSetupSSHKey(t)

	tests := []struct {
		name          string
		isolationJSON string
		want          []string
		wantNil       bool
		wantJSONNull  bool
	}{
		{
			name:          "omitted receives portable defaults",
			isolationJSON: `{"enabled":false}`,
			want:          expectedDefaultIsolationEnvironmentAllowlist,
		},
		{
			name:          "explicit empty remains empty",
			isolationJSON: `{"environment_allowlist":[]}`,
			want:          []string{},
		},
		{
			name:          "explicit null remains programmatic nil",
			isolationJSON: `{"environment_allowlist":null}`,
			wantNil:       true,
			wantJSONNull:  true,
		},
		{
			name:          "custom order remains exact",
			isolationJSON: `{"environment_allowlist":["npm_config_cache","CUSTOM_TOKEN","PathExtra"]}`,
			want:          []string{"npm_config_cache", "CUSTOM_TOKEN", "PathExtra"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeIsolationRuntimeConfig(t, test.isolationJSON)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig(initial) error = %v", err)
			}
			assertIsolationEnvironmentAllowlist(
				t,
				cfg.Isolation.EnvironmentAllowlist,
				test.want,
				test.wantNil,
			)

			if err = SaveConfig(path, cfg); err != nil {
				t.Fatalf("SaveConfig() error = %v", err)
			}
			rawField := readSavedIsolationEnvironmentAllowlist(t, path)
			if test.wantJSONNull {
				if !bytes.Equal(bytes.TrimSpace(rawField), []byte("null")) {
					t.Fatalf("saved environment_allowlist = %s, want null", rawField)
				}
			} else {
				var saved []string
				if err = json.Unmarshal(rawField, &saved); err != nil {
					t.Fatalf("unmarshal saved environment_allowlist: %v", err)
				}
				if saved == nil {
					t.Fatalf("saved environment_allowlist = %s, want JSON array", rawField)
				}
				if !reflect.DeepEqual(saved, test.want) {
					t.Fatalf("saved environment_allowlist = %#v, want %#v", saved, test.want)
				}
			}

			reloaded, loadErr := LoadConfig(path)
			if loadErr != nil {
				t.Fatalf("LoadConfig(saved) error = %v", loadErr)
			}
			assertIsolationEnvironmentAllowlist(
				t,
				reloaded.Isolation.EnvironmentAllowlist,
				test.want,
				test.wantNil,
			)
		})
	}
}

func TestIsolationConfigValidateEnvironmentAllowlist(t *testing.T) {
	maximumNames := make([]string, maxIsolationEnvironmentAllowlistNames)
	for i := range maximumNames {
		maximumNames[i] = "NAME_" + strconv.Itoa(i)
	}

	valid := []struct {
		name  string
		names []string
	}{
		{name: "nil", names: nil},
		{name: "allocated empty", names: []string{}},
		{name: "lowercase and underscore", names: []string{"npm_config_cache", "_private9"}},
		{name: "explicit security capability", names: []string{"OPENAI_API_KEY", "HTTPS_PROXY"}},
		{name: "maximum name bytes", names: []string{"A" + strings.Repeat("0", 127)}},
		{name: "maximum names", names: maximumNames},
	}
	for _, test := range valid {
		t.Run("valid "+test.name, func(t *testing.T) {
			cfg := IsolationConfig{EnvironmentAllowlist: test.names}
			if err := cfg.ValidateEnvironmentAllowlist(); err != nil {
				t.Fatalf("ValidateEnvironmentAllowlist() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name  string
		names []string
		want  string
	}{
		{name: "empty name", names: []string{""}, want: "valid ASCII"},
		{name: "leading digit", names: []string{"9PATH"}, want: "valid ASCII"},
		{name: "equals", names: []string{"NAME=value"}, want: "valid ASCII"},
		{name: "space", names: []string{"BAD NAME"}, want: "valid ASCII"},
		{name: "hyphen", names: []string{"BAD-NAME"}, want: "valid ASCII"},
		{name: "nul", names: []string{"BAD\x00NAME"}, want: "valid ASCII"},
		{name: "control", names: []string{"BAD\nNAME"}, want: "valid ASCII"},
		{name: "non ASCII", names: []string{"N\u00c4ME"}, want: "valid ASCII"},
		{
			name:  "excessive name bytes",
			names: []string{"A" + strings.Repeat("0", maxIsolationEnvironmentNameBytes)},
			want:  "128 bytes",
		},
		{name: "exact duplicate", names: []string{"PATH", "PATH"}, want: "duplicates"},
		{name: "case fold duplicate", names: []string{"Path", "pATH"}, want: "case-insensitively"},
		{
			name:  "excessive names",
			names: append(append([]string(nil), maximumNames...), "NAME_128"),
			want:  "maximum is 128",
		},
	}
	for _, test := range invalid {
		t.Run("invalid "+test.name, func(t *testing.T) {
			cfg := IsolationConfig{EnvironmentAllowlist: test.names}
			err := cfg.ValidateEnvironmentAllowlist()
			if err == nil {
				t.Fatal("ValidateEnvironmentAllowlist() error = nil, want error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateEnvironmentAllowlist() error = %q, want containing %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidIsolationEnvironmentAllowlist(t *testing.T) {
	path := writeIsolationRuntimeConfig(
		t,
		`{"environment_allowlist":["CUSTOM_NAME","custom_name"]}`,
	)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want invalid isolation config")
	}
	if !strings.Contains(err.Error(), "invalid isolation config") ||
		!strings.Contains(err.Error(), "case-insensitively") {
		t.Fatalf("LoadConfig() error = %q, want isolation duplicate context", err)
	}
}

func TestSaveConfigRejectsInvalidIsolationEnvironmentAllowlistBeforePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Isolation.EnvironmentAllowlist = []string{"BAD=NAME"}

	err := SaveConfig(path, cfg)
	if err == nil {
		t.Fatal("SaveConfig() error = nil, want invalid isolation config")
	}
	if !strings.Contains(err.Error(), "invalid isolation config") {
		t.Fatalf("SaveConfig() error = %q, want isolation validation context", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config file was persisted before validation: %v", statErr)
	}
	if _, statErr := os.Stat(securityPath(path)); !os.IsNotExist(statErr) {
		t.Fatalf("security config was persisted before validation: %v", statErr)
	}
}

func writeIsolationRuntimeConfig(t *testing.T, isolationJSON string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"version":` + strconv.Itoa(CurrentVersion) + `,"isolation":` + isolationJSON + `}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(config): %v", err)
	}
	return path
}

func readSavedIsolationEnvironmentAllowlist(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config): %v", err)
	}
	var document struct {
		Isolation struct {
			EnvironmentAllowlist json.RawMessage `json:"environment_allowlist"`
		} `json:"isolation"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if document.Isolation.EnvironmentAllowlist == nil {
		t.Fatal("saved config omitted isolation.environment_allowlist")
	}
	return document.Isolation.EnvironmentAllowlist
}

func assertIsolationEnvironmentAllowlist(
	t *testing.T,
	got []string,
	want []string,
	wantNil bool,
) {
	t.Helper()
	if (got == nil) != wantNil {
		t.Fatalf("environment allowlist nil = %t, want %t; value = %#v", got == nil, wantNil, got)
	}
	if !reflect.DeepEqual(got, want) && !(wantNil && got == nil) {
		t.Fatalf("environment allowlist = %#v, want %#v", got, want)
	}
}
