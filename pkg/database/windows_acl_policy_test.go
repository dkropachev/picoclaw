package database

import "testing"

func TestWindowsOwnerOnlySDDL(t *testing.T) {
	const sid = "S-1-5-21-100-200-300-400"
	file, err := windowsOwnerOnlySDDL(sid, false)
	if err != nil {
		t.Fatal(err)
	}
	if file != "O:"+sid+"D:P(A;;GA;;;"+sid+")" {
		t.Fatalf("file SDDL = %q", file)
	}
	directory, err := windowsOwnerOnlySDDL(sid, true)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "O:"+sid+"D:P(A;OICI;GA;;;"+sid+")" {
		t.Fatalf("directory SDDL = %q", directory)
	}
}

func TestWindowsOwnerOnlySDDLRejectsInjection(t *testing.T) {
	for _, value := range []string{
		"", "BA", "S-1", "S-123", "S-1--2", "S-1-5-21)D:(A;;GA;;;WD", "S-1-5-name",
	} {
		if _, err := windowsOwnerOnlySDDL(value, true); err == nil {
			t.Errorf("SID %q accepted", value)
		}
	}
}
