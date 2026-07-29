package workflows

import "io/fs"

// normalizeWorkflowWindowsTransactionFileMode maps the portable permission
// request used by os.Chmod to the permission bits that os.Stat can observe on
// Windows. Windows represents only the read-only attribute: writable regular
// files report 0666 and read-only regular files report 0444.
func normalizeWorkflowWindowsTransactionFileMode(mode fs.FileMode) fs.FileMode {
	if mode.Perm()&0o200 == 0 {
		return 0o444
	}
	return 0o666
}
