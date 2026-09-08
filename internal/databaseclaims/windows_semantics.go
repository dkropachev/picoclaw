package databaseclaims

const windowsReparsePointAttribute = uint32(0x400)

const windowsClaimMutationMask = uint32(
	0x10000000 | // GENERIC_ALL
		0x40000000 | // GENERIC_WRITE
		0x00010000 | // DELETE
		0x00040000 | // WRITE_DAC
		0x00080000 | // WRITE_OWNER
		0x00000002 | // FILE_WRITE_DATA / FILE_ADD_FILE
		0x00000004 | // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
		0x00000100 | // FILE_WRITE_ATTRIBUTES
		0x00000010 | // FILE_WRITE_EA
		0x00000040, // FILE_DELETE_CHILD
)

func safeWindowsClaimTag(attributes, reparseTag uint32) bool {
	return attributes&windowsReparsePointAttribute == 0 && reparseTag == 0
}

func windowsClaimMutationAccess(mask uint32) bool {
	return mask&windowsClaimMutationMask != 0
}

func classifyWindowsClaimACE(aceType uint8) (allowed, supported bool) {
	switch aceType {
	case 0: // ACCESS_ALLOWED_ACE_TYPE
		return true, true
	case 1: // ACCESS_DENIED_ACE_TYPE
		return false, true
	default:
		return false, false
	}
}

func safeWindowsClaimLinkCount(count uint32) bool {
	return count == 1
}
