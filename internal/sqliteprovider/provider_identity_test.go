package sqliteprovider

import "testing"

func TestInspectedPoolKeyUsesPlatformAliasSemantics(t *testing.T) {
	const path = "/runtime/Store.db"
	for _, test := range []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "/runtime/store.db"},
		{goos: "windows", want: "/runtime/store.db"},
		{goos: "linux", want: path},
	} {
		t.Run(test.goos, func(t *testing.T) {
			if got := inspectedPoolKeyForPlatform(path, test.goos); got != test.want {
				t.Fatalf("inspected pool key = %q, want %q", got, test.want)
			}
		})
	}
}
