package gitworkspace

type pinnedValidationChangeToken struct {
	seconds int64
	nanos   int64
	valid   bool
}

func pinnedValidationTimePart[T ~int | ~int32 | ~int64](value T) int64 {
	return int64(value)
}

func (token pinnedValidationChangeToken) equal(other pinnedValidationChangeToken) bool {
	if !token.valid || !other.valid {
		return !token.valid && !other.valid
	}
	return token.seconds == other.seconds && token.nanos == other.nanos
}
