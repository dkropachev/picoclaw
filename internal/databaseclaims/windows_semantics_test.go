package databaseclaims

import "testing"

func TestWindowsClaimTagRejectsEveryReparseRepresentation(t *testing.T) {
	if !safeWindowsClaimTag(0, 0) {
		t.Fatal("ordinary Windows file was rejected")
	}
	for _, test := range []struct {
		attributes uint32
		tag        uint32
	}{
		{attributes: windowsReparsePointAttribute},
		{tag: 0xa000000c},
		{attributes: windowsReparsePointAttribute, tag: 0x8000001b},
	} {
		if safeWindowsClaimTag(test.attributes, test.tag) {
			t.Fatalf("reparse attributes=%#x tag=%#x accepted", test.attributes, test.tag)
		}
	}
}

func TestWindowsClaimMutationAndACERulesFailClosed(t *testing.T) {
	for _, mask := range []uint32{
		0x10000000, 0x40000000, 0x00010000, 0x00040000, 0x00080000,
		0x2, 0x4, 0x100, 0x10, 0x40,
	} {
		if !windowsClaimMutationAccess(mask) {
			t.Fatalf("Windows mutation right %#x was ignored", mask)
		}
	}
	if windowsClaimMutationAccess(0x80000000 | 0x1 | 0x20) {
		t.Fatal("read/list/traverse rights were treated as mutation")
	}
	if allowed, supported := classifyWindowsClaimACE(0); !allowed || !supported {
		t.Fatal("ordinary allow ACE was not classified")
	}
	if allowed, supported := classifyWindowsClaimACE(1); allowed || !supported {
		t.Fatal("ordinary deny ACE was not classified")
	}
	for _, aceType := range []uint8{5, 6, 9, 10, 11, 12, 0xff} {
		if _, supported := classifyWindowsClaimACE(aceType); supported {
			t.Fatalf("unsupported ACE type %#x was accepted", aceType)
		}
	}
}

func TestWindowsClaimLockRequiresExactlyOneLink(t *testing.T) {
	if !safeWindowsClaimLinkCount(1) {
		t.Fatal("single-link claim lock was rejected")
	}
	for _, count := range []uint32{0, 2, 1 << 31, ^uint32(0)} {
		if safeWindowsClaimLinkCount(count) {
			t.Fatalf("claim lock link count %d accepted", count)
		}
	}
}
