//go:build !unix && !windows

package database

func consumeSupervisorBootstrapFile(string, supervisorFileIdentity, string, string) error {
	return NewError(CodeUnsupported, "database supervisor bootstrap consumption is unsupported")
}

func consumeSupervisorBootstrapFileWithHook(string, supervisorFileIdentity, string, func()) error {
	return NewError(CodeUnsupported, "database supervisor bootstrap consumption is unsupported")
}

func consumeSupervisorBootstrapFileExpected(
	string,
	supervisorFileIdentity,
	string,
	supervisorFileIdentity,
) error {
	return NewError(CodeUnsupported, "database supervisor bootstrap consumption is unsupported")
}
