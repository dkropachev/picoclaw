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

	bootstrap := filepath.Join(stateDir, ".bootstrap-test")
	bootstrapFile, err := createOwnerOnlyExclusiveFile(bootstrap, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapFile.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, bootstrap)

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

func TestWindowsManifestBootstrapAndLocksAreOwnerOnly(t *testing.T) {
	home := t.TempDir()
	bootstrap, err := prepareSupervisorBootstrap(home, strings.Repeat("a", tokenBytes*2))
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsOwnerOnlyFile(t, bootstrap)
	if err := os.Remove(bootstrap); err != nil {
		t.Fatal(err)
	}

	stateDir, err := StateDirectory(home)
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

func TestWindowsOwnerValidationUsesCurrentUserSeam(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), StateDirectoryName)
	if err := createOwnerOnlyDirectory(stateDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	original := windowsCurrentProcessUserSID
	windowsCurrentProcessUserSID = func() (*windows.SID, error) { return foreign, nil }
	t.Cleanup(func() { windowsCurrentProcessUserSID = original })
	if err := validateOwnerOnlyDirectory(stateDir, info); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("foreign owner error = %v, want Unauthorized", err)
	}
}

func TestWindowsOwnerValidationRejectsAdditionalTrustee(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), StateDirectoryName)
	if err := createOwnerOnlyDirectory(stateDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "permissive.lock")
	file, err := createOwnerOnlyExclusiveFile(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
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
