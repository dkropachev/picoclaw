//go:build windows

package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsOwnerOnlyStateAndFiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), StateDirectoryName)
	if err := createOwnerOnlyDirectory(stateDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlyDirectory(stateDir, info); err != nil {
		t.Fatalf("owner-only state directory rejected: %v", err)
	}

	bootstrapFile, err := createOwnerOnlyTempFile(stateDir, ".bootstrap-test-", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapFile.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, bootstrapFile.Name())

	temporary, err := createOwnerOnlyTempFile(stateDir, ".manifest-", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, temporaryPath)

	lockPath := filepath.Join(stateDir, storageLockFileName)
	lock, err := openOwnerOnlyLockFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, lockPath)
}

func TestWindowsManifestAndLocksAreOwnerOnly(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		PID:      os.Getpid(),
		Protocol: ProtocolVersion,
		Token:    strings.Repeat("b", tokenBytes*2),
		Endpoint: endpointForStateDirectory(stateDir),
		Epoch:    strings.Repeat("c", epochBytes*2),
	}
	if err := writeManifest(stateDir, manifest); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, filepath.Join(stateDir, manifestFileName))
	if discovered, err := ReadManifest(home); err != nil || discovered != manifest {
		t.Fatalf("ReadManifest() = %#v, %v", discovered, err)
	}

	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, filepath.Join(stateDir, storageLockFileName))

	singleton, err := acquireBrokerSingleton(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := singleton.close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, filepath.Join(stateDir, brokerLockFileName))
}

func TestWindowsEndpointAndManifestAcceptCaseAlias(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), StateDirectoryName)
	alias := strings.ToUpper(stateDir)
	endpoint := endpointForStateDirectory(stateDir)
	if alias == stateDir {
		t.Skip("temporary path has no case-distinct alias")
	}
	if got := endpointForStateDirectory(alias); got != endpoint {
		t.Fatalf("case-alias endpoint = %q, want %q", got, endpoint)
	}
	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion,
		Token: strings.Repeat("a", tokenBytes*2), Endpoint: endpoint,
		Epoch: strings.Repeat("b", epochBytes*2),
	}
	if err := validateManifest(manifest, alias); err != nil {
		t.Fatalf("case-alias manifest rejected: %v", err)
	}
}

func TestWindowsTrustedHomeRejectsUntrustedWriteAccess(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := createOwnerOnlyDirectory(home); err != nil {
		t.Fatal(err)
	}
	current, err := currentWindowsProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	setAccess := func(access windows.ACCESS_MASK) {
		t.Helper()
		entries := []windows.EXPLICIT_ACCESS{
			windowsFullControlEntry(current),
			{
				AccessPermissions: access,
				AccessMode:        windows.GRANT_ACCESS,
				Trustee: windows.TRUSTEE{
					TrusteeForm:  windows.TRUSTEE_IS_SID,
					TrusteeValue: windows.TrusteeValueFromSID(world),
				},
			},
		}
		acl, aclErr := windows.ACLFromEntries(entries, nil)
		if aclErr != nil {
			t.Fatal(aclErr)
		}
		if setErr := windows.SetNamedSecurityInfo(
			home,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			acl,
			nil,
		); setErr != nil {
			t.Fatal(setErr)
		}
	}
	setAccess(windows.GENERIC_READ)
	info, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedHomeDirectory(home, info); err != nil {
		t.Fatalf("read-only public home grant rejected: %v", err)
	}
	setAccess(windows.GENERIC_WRITE)
	if err := validateTrustedHomeDirectory(home, info); CodeOf(err) != CodeIntegrity {
		t.Fatalf("writable public home grant error = %v", err)
	}
}

func TestWindowsOwnerValidationRejectsAdditionalTrustee(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), StateDirectoryName)
	if err := createOwnerOnlyDirectory(stateDir); err != nil {
		t.Fatal(err)
	}
	file, err := createOwnerOnlyTempFile(stateDir, "permissive-", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	owner, err := currentWindowsProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		windowsFullControlEntry(owner),
		windowsFullControlEntry(world),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlyFile(path, info, 0o600); CodeOf(err) != CodeIntegrity {
		t.Fatalf("permissive DACL error = %v, want Integrity", err)
	}
}

func windowsFullControlEntry(sid *windows.SID) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func assertWindowsOwnerOnlyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlyFile(path, info, 0o600); err != nil {
		t.Fatalf("owner-only file %q rejected: %v", path, err)
	}
	file, err := openOwnerOnlyExistingFile(path, 0o600)
	if err != nil {
		t.Fatalf("owner-only file handle %q rejected: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
