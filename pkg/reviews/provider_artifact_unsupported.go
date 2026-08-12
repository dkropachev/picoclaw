//go:build windows || js || plan9

package reviews

import "errors"

func acquireProviderArtifact(
	string,
	string,
	func(string),
) (*providerArtifact, error) {
	return nil, errors.New("safe provider artifact consumption is unavailable on this platform")
}
