package workflows

import (
	"io/fs"
	"testing"
)

func TestNormalizeWorkflowWindowsTransactionFileMode(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		want fs.FileMode
	}{
		{name: "owner_writable_0600", mode: 0o600, want: 0o666},
		{name: "owner_writable_0644", mode: 0o644, want: 0o666},
		{name: "all_writable", mode: 0o777, want: 0o666},
		{name: "owner_read_only", mode: 0o400, want: 0o444},
		{name: "all_read_only", mode: 0o444, want: 0o444},
		{name: "no_permissions", mode: 0, want: 0o444},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeWorkflowWindowsTransactionFileMode(test.mode); got != test.want {
				t.Fatalf("normalized mode = %04o, want %04o", got, test.want)
			}
		})
	}
}
