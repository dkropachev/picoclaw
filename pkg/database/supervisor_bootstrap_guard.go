package database

import (
	"os"
	"path/filepath"
)

const supervisorBootstrapGuardFailure = "database supervisor bootstrap home generation changed"

// SupervisorBootstrapGuard retains the exact physical home and private state
// directory generation that contained one successfully consumed bootstrap.
// Its fields are intentionally opaque and immutable; Validate is safe for
// concurrent repeated use and performs no filesystem mutation.
type SupervisorBootstrapGuard struct {
	home          string
	homeIdentity  supervisorFileIdentity
	stateDir      string
	stateIdentity supervisorFileIdentity
}

// ConsumeSupervisorBootstrapGuard consumes the one-use bootstrap inherited by
// a hidden child and returns its exact physical-home guard. Every authority
// environment value is removed before validation. Callers must Validate the
// guard immediately before startup and again from ServerOptions.StartupGuard.
func ConsumeSupervisorBootstrapGuard(home string) (*SupervisorBootstrapGuard, error) {
	return consumeSupervisorBootstrapGuardWith(
		home,
		consumeSupervisorBootstrapEnvironment(),
		defaultSupervisorBootstrapGuardOps(),
	)
}

type supervisorBootstrapBoundary struct {
	home          string
	homeIdentity  supervisorFileIdentity
	stateDir      string
	stateIdentity supervisorFileIdentity
}

type supervisorBootstrapBoundaryOps struct {
	canonicalHome     func(string) (string, error)
	stateDirectory    func(string) (string, error)
	directoryIdentity func(string) (supervisorFileIdentity, error)
}

type supervisorBootstrapGuardOps struct {
	boundary   supervisorBootstrapBoundaryOps
	consume    func(string, supervisorBootstrapAuthority, supervisorBootstrapConsumeOps) bool
	consumeOps supervisorBootstrapConsumeOps
}

func defaultSupervisorBootstrapGuardOps() supervisorBootstrapGuardOps {
	return supervisorBootstrapGuardOps{
		boundary: supervisorBootstrapBoundaryOps{
			canonicalHome: CanonicalHome, stateDirectory: StateDirectory,
			directoryIdentity: supervisorDirectoryIdentity,
		},
		consume: consumeSupervisorBootstrapWith,
		consumeOps: supervisorBootstrapConsumeOps{
			executable:        os.Executable,
			pathIdentity:      supervisorPathIdentity,
			stateDirectory:    StateDirectory,
			directoryIdentity: supervisorDirectoryIdentity,
			consume:           consumeSupervisorBootstrapFile,
		},
	}
}

func consumeSupervisorBootstrapGuardWith(
	home string,
	authority supervisorBootstrapAuthority,
	ops supervisorBootstrapGuardOps,
) (*SupervisorBootstrapGuard, error) {
	if !validSupervisorBootstrapGuardOps(ops) {
		return nil, NewError(CodeInvalid, "database supervisor bootstrap guard operations are invalid")
	}
	if !validLowerHex(authority.token, tokenBytes*2) || authority.bootstrapIdentity == "" ||
		authority.executableIdentity == "" {
		return nil, NewError(CodeUnauthorized, "database supervisor bootstrap authority is unavailable")
	}
	if !supervisorBootstrapImageAuthorityValid(authority, ops.consumeOps) {
		return nil, NewError(CodeUnauthorized, "database supervisor bootstrap authority is unavailable")
	}
	before, err := captureSupervisorBootstrapBoundary(home, ops.boundary)
	if err != nil {
		return nil, err
	}
	if !ops.consume(home, authority, ops.consumeOps) {
		return nil, NewError(CodeUnauthorized, "database supervisor bootstrap authority is unavailable")
	}
	after, err := captureSupervisorBootstrapBoundary(home, ops.boundary)
	if err != nil || before != after {
		return nil, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	guard := &SupervisorBootstrapGuard{
		home: before.home, homeIdentity: before.homeIdentity,
		stateDir: before.stateDir, stateIdentity: before.stateIdentity,
	}
	if err := validateSupervisorBootstrapGuardWith(guard, home, ops.boundary); err != nil {
		return nil, err
	}
	return guard, nil
}

func supervisorBootstrapImageAuthorityValid(
	authority supervisorBootstrapAuthority,
	ops supervisorBootstrapConsumeOps,
) bool {
	if ops.executable == nil || ops.pathIdentity == nil ||
		!validLowerHex(authority.token, tokenBytes*2) || authority.bootstrapIdentity == "" ||
		authority.executableIdentity == "" {
		return false
	}
	currentExecutable, err := ops.executable()
	if err != nil {
		return false
	}
	currentIdentity, err := ops.pathIdentity(currentExecutable)
	return err == nil && currentIdentity.Valid() &&
		currentIdentity.String() == authority.executableIdentity
}

func validSupervisorBootstrapGuardOps(ops supervisorBootstrapGuardOps) bool {
	return ops.consume != nil && ops.consumeOps.executable != nil &&
		ops.consumeOps.pathIdentity != nil && ops.consumeOps.stateDirectory != nil &&
		ops.consumeOps.directoryIdentity != nil && ops.consumeOps.consume != nil &&
		validSupervisorBootstrapBoundaryOps(ops.boundary)
}

func validSupervisorBootstrapBoundaryOps(ops supervisorBootstrapBoundaryOps) bool {
	return ops.canonicalHome != nil && ops.stateDirectory != nil && ops.directoryIdentity != nil
}

func captureSupervisorBootstrapBoundary(
	home string,
	ops supervisorBootstrapBoundaryOps,
) (supervisorBootstrapBoundary, error) {
	if !validSupervisorBootstrapBoundaryOps(ops) {
		return supervisorBootstrapBoundary{}, NewError(
			CodeInvalid,
			"database supervisor bootstrap boundary operations are invalid",
		)
	}
	first, err := inspectSupervisorBootstrapBoundary(home, ops)
	if err != nil {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	second, err := inspectSupervisorBootstrapBoundary(home, ops)
	if err != nil || first != second {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	return first, nil
}

func inspectSupervisorBootstrapBoundary(
	home string,
	ops supervisorBootstrapBoundaryOps,
) (supervisorBootstrapBoundary, error) {
	canonical, err := ops.canonicalHome(home)
	if err != nil || canonical == "" || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	homeIdentity, err := ops.directoryIdentity(canonical)
	if err != nil || !homeIdentity.Valid() {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	stateDir, err := ops.stateDirectory(canonical)
	if err != nil || stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir ||
		!sameCanonicalPath(stateDir, filepath.Join(canonical, StateDirectoryName)) {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	stateIdentity, err := ops.directoryIdentity(stateDir)
	if err != nil || !stateIdentity.Valid() {
		return supervisorBootstrapBoundary{}, NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	return supervisorBootstrapBoundary{
		home: canonical, homeIdentity: homeIdentity,
		stateDir: stateDir, stateIdentity: stateIdentity,
	}, nil
}

// Validate confirms that home still resolves to the exact trusted home and
// private state directory generation retained at bootstrap consumption.
func (guard *SupervisorBootstrapGuard) Validate(home string) error {
	return validateSupervisorBootstrapGuardWith(
		guard,
		home,
		defaultSupervisorBootstrapGuardOps().boundary,
	)
}

func validateSupervisorBootstrapGuardWith(
	guard *SupervisorBootstrapGuard,
	home string,
	ops supervisorBootstrapBoundaryOps,
) error {
	if guard == nil || guard.home == "" || guard.stateDir == "" ||
		!guard.homeIdentity.Valid() || !guard.stateIdentity.Valid() ||
		!sameCanonicalPath(guard.stateDir, filepath.Join(guard.home, StateDirectoryName)) {
		return NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	current, err := captureSupervisorBootstrapBoundary(home, ops)
	if err != nil || !sameCanonicalPath(current.home, guard.home) ||
		current.homeIdentity != guard.homeIdentity ||
		!sameCanonicalPath(current.stateDir, guard.stateDir) ||
		current.stateIdentity != guard.stateIdentity {
		return NewError(CodeIntegrity, supervisorBootstrapGuardFailure)
	}
	return nil
}
