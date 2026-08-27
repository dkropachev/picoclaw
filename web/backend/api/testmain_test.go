package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	apiTestHomeEnv      = "_PICOCLAW_API_TEST_HOME"
	apiTestHomeOwnerEnv = "_PICOCLAW_API_TEST_HOME_OWNER_PID"
)

// TestMain keeps package tests away from the operator's live PicoClaw home.
// Several API helpers discover and manage gateway processes through the global
// PID file, so inheriting a developer's PICOCLAW_HOME can give tests authority
// over a real gateway process.
func TestMain(m *testing.M) {
	isolatedHome, ownsHome, err := isolatedAPITestHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated PicoClaw test home: %v\n", err)
		os.Exit(1)
	}

	if err = os.Setenv(config.EnvHome, isolatedHome); err != nil {
		fmt.Fprintf(os.Stderr, "set isolated PicoClaw test home: %v\n", err)
		if ownsHome {
			_ = os.RemoveAll(isolatedHome)
		}
		os.Exit(1)
	}

	code := m.Run()
	if ownsHome {
		err = os.RemoveAll(isolatedHome)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated PicoClaw test home: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	// Keep PICOCLAW_HOME pointed at the now-removed sandbox until process exit.
	// Restoring an operator path would reopen access for a leaked goroutine.
	os.Exit(code)
}

func isolatedAPITestHome() (string, bool, error) {
	inheritedHome := os.Getenv(apiTestHomeEnv)
	ownerPID, ownerErr := strconv.Atoi(os.Getenv(apiTestHomeOwnerEnv))
	if ownerErr == nil && ownerPID == os.Getppid() && filepath.IsAbs(inheritedHome) {
		return inheritedHome, false, nil
	}

	isolatedHome, err := os.MkdirTemp("", "picoclaw-api-test-home-")
	if err != nil {
		return "", false, err
	}
	if err = os.Setenv(apiTestHomeEnv, isolatedHome); err != nil {
		_ = os.RemoveAll(isolatedHome)
		return "", false, err
	}
	if err = os.Setenv(apiTestHomeOwnerEnv, strconv.Itoa(os.Getpid())); err != nil {
		_ = os.RemoveAll(isolatedHome)
		return "", false, err
	}
	return isolatedHome, true, nil
}
