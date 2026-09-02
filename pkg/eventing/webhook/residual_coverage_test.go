package webhook

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestWebhookHeaderRuntimeAndDecoderResidualMatrix(t *testing.T) {
	unknown := connectorRuntime{format: 99}
	if _, ok := unknown.authenticationHeaders(http.Header{}); ok ||
		unknown.verify(nil, nil, admissionAuthentication{}) {
		t.Fatal("unknown connector authenticated")
	}
	if _, _, err := unknown.decode(nil, admissionAuthentication{}); err == nil {
		t.Fatal("unknown connector decoded")
	}
	backend := &Backend{secretValues: []string{"secret", "token"}}
	if !backend.identityContainsSecret("prefix-secret-suffix") || backend.identityContainsSecret("safe") {
		t.Fatal("secret identity classification mismatch")
	}
	if first(nil) != "" || first([]string{"one", "two"}) != "one" {
		t.Fatal("first header value mismatch")
	}
	headers := http.Header{}
	if _, ok := exactlyOneHeader(headers, "X-Test"); ok {
		t.Fatal("missing header accepted")
	}
	headers["x-test"] = []string{"one", "two"}
	if _, ok := exactlyOneHeader(headers, "X-Test"); ok {
		t.Fatal("duplicate header accepted")
	}
	for _, values := range [][]string{nil, {"identity"}, {" Identity "}} {
		headers = http.Header{"Content-Encoding": values}
		if !identityEncoding(headers) {
			t.Errorf("identity encoding rejected: %#v", values)
		}
	}
	for _, values := range [][]string{{"gzip"}, {"identity", "identity"}} {
		if identityEncoding(http.Header{"Content-Encoding": values}) {
			t.Errorf("nonidentity encoding accepted: %#v", values)
		}
	}
	for value, want := range map[string]bool{
		"application/json": true, "application/json; charset=utf-8": true,
		"text/plain": false, "application/json; profile=x": false, "bad[": false,
	} {
		if got := jsonContentType(http.Header{"Content-Type": []string{value}}); got != want {
			t.Errorf("jsonContentType(%q) = %v", value, got)
		}
	}

	valid := `{"type":"event","occurred_at":"2026-09-02T12:00:00Z","actor":{"id":"a","type":"user","display_name":"A","attributes":{"key":"value"}},"subject":{"id":"s","type":"repo","name":"R","url":"https://example.test","attributes":{"key":"value"}},"attributes":{"key":"value"},"payload":{"ok":true}}`
	if request, err := decodeAdmissionRequest([]byte(valid)); err != nil || request.eventType != "event" ||
		request.actor == nil || request.subject == nil {
		t.Fatalf("valid admission = %#v, %v", request, err)
	}
	invalid := []string{
		string([]byte{0xff}), `[]`, `{`, `{}`, `{"type":"event"}`, `{"payload":{}}`,
		`{"type":"event","type":"again","payload":{}}`,
		`{"unknown":true,"type":"event","payload":{}}`,
		`{"type":1,"payload":{}}`, `{"type":"event","payload":[]} trailing`,
	}
	for _, raw := range invalid {
		if _, err := decodeAdmissionRequest([]byte(raw)); err == nil {
			t.Errorf("invalid admission accepted: %q", raw)
		}
	}
	for _, raw := range []string{`1`, `"bad`, `null`} {
		if _, err := decodeString([]byte(raw)); err == nil {
			t.Errorf("invalid string accepted: %q", raw)
		}
	}
	for _, raw := range []string{`1`, `"bad"`} {
		if _, err := decodeTimestamp([]byte(raw)); err == nil {
			t.Errorf("invalid timestamp accepted: %q", raw)
		}
	}
	for _, raw := range []string{`[]`, `{"unknown":1}`, `{"id":"a","id":"b"}`, `{"id":1}`} {
		if _, err := decodeActor([]byte(raw)); err == nil {
			t.Errorf("invalid actor accepted: %q", raw)
		}
		if _, err := decodeSubject([]byte(raw)); err == nil {
			t.Errorf("invalid subject accepted: %q", raw)
		}
	}
	allowed := map[string]struct{}{"id": {}}
	for _, raw := range []string{`[]`, `{"unknown":1}`, `{"id":1,"id":2}`, `{"id":`} {
		if _, err := decodeObject([]byte(raw), allowed); err == nil {
			t.Errorf("invalid object accepted: %q", raw)
		}
	}
	for _, raw := range []string{`[]`, `{"key":1}`, `{"key":"a","key":"b"}`, `{"key":`} {
		if _, err := decodeStringMap([]byte(raw)); err == nil {
			t.Errorf("invalid string map accepted: %q", raw)
		}
	}
	for _, raw := range []string{`[]`, `null`, `{`} {
		if _, err := decodePayload([]byte(raw)); err == nil {
			t.Errorf("invalid payload accepted: %q", raw)
		}
	}
}

func TestWebhookGitHubConfigurationAndProjectionResidualMatrix(t *testing.T) {
	if _, err := normalizedGitHubRepositories(make([]string, maximumGitHubScopeRepositories+1)); err == nil {
		t.Fatal("too many repositories accepted")
	}
	for _, repositories := range [][]string{{"bad"}, {"Owner/Repo", "owner/repo"}} {
		if _, err := normalizedGitHubRepositories(repositories); err == nil {
			t.Errorf("invalid repositories accepted: %#v", repositories)
		}
	}
	if values, err := normalizedGitHubRepositories(nil); err != nil || values != nil {
		t.Fatalf("empty repositories = %#v, %v", values, err)
	}
	for _, value := range []string{" bad ", "-bad", "bad-", string([]byte{0xff})} {
		if _, err := normalizedGitHubTargetUser(value); err == nil {
			t.Errorf("invalid target user accepted: %q", value)
		}
	}
	if value, err := normalizedGitHubTargetUser("User-1"); err != nil || value != "user-1" {
		t.Fatalf("normalized target = %q, %v", value, err)
	}
	for _, repository := range []string{"", " owner/repo", "owner", "owner/repo/extra", "owner/re@po"} {
		if validGitHubRepository(repository) {
			t.Errorf("invalid repository accepted: %q", repository)
		}
	}
	if validGitHubRepositorySegment("bad@segment") {
		t.Fatal("invalid repository segment accepted")
	}
	for _, format := range []string{"unsupported"} {
		if _, err := runtimeForConnector(format, "secret"); err == nil {
			t.Errorf("unsupported format %q accepted", format)
		}
	}
	for _, secret := range []string{"bad", "whsec_%%%", "whsec_" + base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := verifierForSecret(secret); err == nil {
			t.Errorf("invalid standard secret accepted")
		}
	}
	standardSecret := "whsec_" + base64.StdEncoding.EncodeToString(
		[]byte(strings.Repeat("s", minimumStandardSecretBytes)),
	)
	if runtime, err := runtimeForConnector("standard", standardSecret); err != nil || runtime.standardVerifier == nil {
		t.Fatalf("standard runtime = %#v, %v", runtime, err)
	}
	for _, secret := range []string{"short", " bad-secret ", string([]byte{0xff})} {
		if _, err := githubSecretBytes(secret); err == nil {
			t.Errorf("invalid GitHub secret accepted")
		}
	}
	githubSecret := strings.Repeat("g", minimumGitHubSecretBytes)
	if runtime, err := runtimeForConnector("github", githubSecret); err != nil ||
		string(runtime.githubSecret) != githubSecret {
		t.Fatalf("GitHub runtime = %#v, %v", runtime, err)
	}
	if githubUsersContain(nil, "user") || githubUsersContain([]githubUserPayload{{Login: "other"}}, "user") ||
		!githubUsersContain([]githubUserPayload{{Login: "USER"}}, "user") {
		t.Fatal("GitHub user containment mismatch")
	}
	if verifyGitHubSignature(nil, nil, nil) {
		t.Fatal("empty GitHub signature verified")
	}
	for _, body := range []string{string([]byte{0xff}), `[]`, `{`, `{} {}`, `{"x":1,"x":2}`} {
		if _, _, err := decodeGitHubObject([]byte(body)); err == nil {
			t.Errorf("invalid GitHub object accepted: %q", body)
		}
	}
}
