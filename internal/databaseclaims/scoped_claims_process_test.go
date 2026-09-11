package databaseclaims

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewClaimCrashChildEnvironment  = "PICOCLAW_REVIEW_CLAIM_CRASH_CHILD"
	reviewClaimCrashParentEnvironment = "PICOCLAW_REVIEW_CLAIM_CRASH_PARENT"
	reviewClaimCrashHomeEnvironment   = "PICOCLAW_REVIEW_CLAIM_CRASH_HOME"
	reviewClaimCrashUserEnvironment   = "PICOCLAW_REVIEW_CLAIM_CRASH_USER_HOME"
	reviewClaimCrashRootEnvironment   = "PICOCLAW_REVIEW_CLAIM_CRASH_ROOT"
)

func TestReviewScopeClaimsReleaseAfterProcessCrash(t *testing.T) {
	if os.Getenv(reviewClaimCrashChildEnvironment) == "1" {
		runReviewScopeClaimCrashChild(t)
		return
	}

	home := secureTestDir(t)
	userHome := secureTestDir(t)
	claimRoot := isolatedTestClaimRoot(t)
	options := reviewScopeCrashOptions(home, userHome)
	scope, err := storecatalog.NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	assertReviewScopeTestOwned(t, scope, home, userHome)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	arguments := []string{"-test.run=^TestReviewScopeClaimsReleaseAfterProcessCrash$"}
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "-test.gocoverdir=") {
			arguments = append(arguments, argument)
			break
		}
	}
	command := exec.CommandContext(ctx, os.Args[0], arguments...)
	command.Env = append(
		os.Environ(),
		reviewClaimCrashChildEnvironment+"=1",
		reviewClaimCrashParentEnvironment+"="+strconv.Itoa(os.Getpid()),
		reviewClaimCrashHomeEnvironment+"="+home,
		reviewClaimCrashUserEnvironment+"="+userHome,
		reviewClaimCrashRootEnvironment+"="+claimRoot,
	)
	childInput, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if startErr := command.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		_ = childInput.Close()
	})
	ready := make(chan string, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			ready <- fmt.Sprintf("read child readiness: %v", readErr)
			return
		}
		ready <- strings.TrimSpace(line)
	}()
	select {
	case marker := <-ready:
		if marker != "ready" {
			t.Fatalf("review claim child readiness = %q; stderr=%s", marker, stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("review claim child readiness timed out: %v; stderr=%s", ctx.Err(), stderr.String())
	}

	assertReviewScopeClaimFiles(t, claimRoot, full, selected)
	competing, err := AcquireReviewScopeForTesting(scope, fence, claimRoot)
	if competing != nil || database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("lease while child owns selected claims = %#v, %v", competing, err)
	}
	if killErr := command.Process.Kill(); killErr != nil {
		t.Fatalf("crash review claim child: %v", killErr)
	}
	if waitErr := command.Wait(); waitErr == nil {
		t.Fatal("crashed review claim child exited successfully")
	}
	waited = true

	recovered, err := AcquireReviewScopeForTesting(scope, fence, claimRoot)
	if err != nil || recovered == nil {
		t.Fatalf("lease after child crash = %#v, %v; stderr=%s", recovered, err, stderr.String())
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func runReviewScopeClaimCrashChild(t *testing.T) {
	parentPID, err := strconv.Atoi(os.Getenv(reviewClaimCrashParentEnvironment))
	if err != nil || parentPID <= 0 || os.Getppid() != parentPID {
		t.Fatal("review claim crash child authority is invalid")
	}
	home := os.Getenv(reviewClaimCrashHomeEnvironment)
	userHome := os.Getenv(reviewClaimCrashUserEnvironment)
	claimRoot := os.Getenv(reviewClaimCrashRootEnvironment)
	scope, err := storecatalog.NewReviewScope(reviewScopeCrashOptions(home, userHome), "missing")
	if err != nil {
		t.Fatal(err)
	}
	assertReviewScopeTestOwned(t, scope, home, userHome)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := AcquireReviewScopeForTesting(scope, fence, claimRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	var hold [1]byte
	if _, readErr := os.Stdin.Read(hold[:]); readErr != nil {
		t.Fatalf("review claim crash child hold pipe closed: %v", readErr)
	}
	t.Fatal("review claim crash child was released without a crash")
}

func reviewScopeCrashOptions(home, userHome string) storecatalog.Options {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	return storecatalog.Options{Home: home, Config: cfg, UserHome: userHome}
}

func assertReviewScopeClaimFiles(
	t *testing.T,
	root string,
	full *storecatalog.Catalog,
	selected *storecatalog.Catalog,
) {
	t.Helper()
	fullIDs, err := full.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	selectedIDs, err := selected.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	selectedSet := make(map[storecatalog.ClaimID]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selectedSet[id] = struct{}{}
		if _, err := os.Lstat(filepath.Join(root, id.String()+".lock")); err != nil {
			t.Fatalf("child selected claim %q is absent: %v", id, err)
		}
	}
	unselected := 0
	for _, id := range fullIDs {
		if _, chosen := selectedSet[id]; chosen {
			continue
		}
		unselected++
		if _, err := os.Lstat(filepath.Join(root, id.String()+".lock")); !os.IsNotExist(err) {
			t.Fatalf("child created unselected claim %q: %v", id, err)
		}
	}
	if unselected == 0 {
		t.Fatal("review scope has no unselected claims to test")
	}
}
