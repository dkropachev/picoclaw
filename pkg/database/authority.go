package database

import (
	"context"
	"encoding/base64"
	"os"
	"time"
)

const inheritedAuthorityEnvironment = "PICOCLAW_DATABASE_AUTHORITY"

type inheritedAuthority struct {
	Home     string   `json:"home"`
	Manifest Manifest `json:"manifest"`
}

// InheritedAuthorityEnvironment serializes current owner-only discovery for a
// direct trusted child. The value contains no filesystem database identity.
func InheritedAuthorityEnvironment(home string) (string, error) {
	canonical, err := CanonicalHome(home)
	if err != nil {
		return "", err
	}
	manifest, err := ReadManifest(canonical)
	if err != nil {
		return "", err
	}
	raw, err := MarshalCanonical(inheritedAuthority{Home: canonical, Manifest: manifest})
	if err != nil {
		return "", err
	}
	return inheritedAuthorityEnvironment + "=" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ConnectInherited consumes and verifies private runtime-child authority before
// any runtime config, provider, or channel initialization.
func ConnectInherited(ctx context.Context) (*Client, string, error) {
	encoded := os.Getenv(inheritedAuthorityEnvironment)
	_ = os.Unsetenv(inheritedAuthorityEnvironment)
	if encoded == "" {
		return nil, "", NewError(CodeUnauthorized, "database broker child authority is missing")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return nil, "", NewError(CodeUnauthorized, "database broker child authority is invalid")
	}
	var authority inheritedAuthority
	if decodeErr := UnmarshalCanonical(raw, &authority); decodeErr != nil {
		return nil, "", NewError(CodeUnauthorized, "database broker child authority is invalid")
	}
	client, err := ConnectWithManifest(authority.Home, authority.Manifest)
	if err != nil {
		return nil, "", NewError(CodeUnauthorized, "database broker child authority is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := client.Ping(pingCtx); err != nil {
		return nil, "", NewError(CodeUnavailable, "database broker child authority is unavailable")
	}
	InstallProcessClient(client)
	return client, authority.Home, nil
}
