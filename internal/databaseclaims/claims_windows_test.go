//go:build windows

package databaseclaims

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWindowsClaimLeasePoisonsAfterRootDACLMutation(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	owner, err := currentWindowsClaimSID()
	if err != nil {
		t.Fatal(err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		windowsClaimTestAccess(owner, windows.GENERIC_ALL),
		windowsClaimTestAccess(world, windows.GENERIC_READ),
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		testClaims.root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Check after claim-root DACL mutation = %v", err)
	}
	if _, err := prepareExplicitTestClaimRoot(testClaims.root); err != nil {
		t.Fatalf("restore test claim root DACL: %v", err)
	}
}

func TestWindowsPrepareTestRootRejectsAliasBeforeMutation(t *testing.T) {
	target := t.TempDir()
	before := windowsClaimTestDescriptor(t, target)
	alias := filepath.Join(filepath.Dir(target), filepath.Base(target)+"-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	if root, err := PrepareRootForTesting(alias); root != "" || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PrepareRootForTesting(alias) = %q, %v", root, err)
	}
	if after := windowsClaimTestDescriptor(t, target); after != before {
		t.Fatalf("rejected alias mutated target DACL: before=%q after=%q", before, after)
	}
}

func windowsClaimTestAccess(sid *windows.SID, access windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: access,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func windowsClaimTestDescriptor(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}
