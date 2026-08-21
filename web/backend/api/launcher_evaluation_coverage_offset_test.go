package api

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestLauncherEvaluationCoverageOffsetModelAndAttachmentHelpers(t *testing.T) {
	expression := &config.AccountRouterExpression{
		Op: "add",
		Left: &config.AccountRouterExpression{
			Account: " primary ",
		},
		Right: &config.AccountRouterExpression{
			Op: "multiply",
			Left: &config.AccountRouterExpression{
				Account: "secondary",
			},
		},
	}
	condition := &config.AccountRouterCondition{
		Left: *expression,
		Right: config.AccountRouterExpression{
			Account: "fallback",
		},
	}
	if !accountRouterConditionReferences(condition, "primary") ||
		!accountRouterConditionReferences(condition, "secondary") ||
		!accountRouterConditionReferences(condition, "fallback") ||
		accountRouterConditionReferences(condition, "missing") ||
		accountRouterConditionReferences(nil, "primary") ||
		accountRouterExpressionReferences(nil, "primary") {
		t.Fatal("account-router reference traversal returned an invalid result")
	}

	for _, test := range []struct {
		name     string
		apiBase  string
		wantRoot string
		wantHost string
	}{
		{name: "https default", apiBase: " https://api.example.test/v1 ", wantRoot: "https://api.example.test", wantHost: "api.example.test:443"},
		{name: "http default", apiBase: "http://api.example.test/v1", wantRoot: "http://api.example.test", wantHost: "api.example.test:80"},
		{name: "explicit port", apiBase: "https://api.example.test:8443/v1", wantRoot: "https://api.example.test:8443", wantHost: "api.example.test:8443"},
		{name: "schemeless", apiBase: "localhost:11434/api", wantRoot: "http://localhost:11434", wantHost: "localhost:11434"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, rootErr := apiRootFromAPIBase(test.apiBase)
			host, hostErr := hostPortFromAPIBase(test.apiBase)
			if rootErr != nil || hostErr != nil || root != test.wantRoot || host != test.wantHost {
				t.Fatalf("api base %q -> root=%q host=%q errors=%v/%v", test.apiBase, root, host, rootErr, hostErr)
			}
		})
	}
	for _, invalid := range []string{"", "   ", "://"} {
		if _, err := apiRootFromAPIBase(invalid); err == nil {
			t.Fatalf("apiRootFromAPIBase(%q) accepted invalid value", invalid)
		}
		if _, err := hostPortFromAPIBase(invalid); err == nil {
			t.Fatalf("hostPortFromAPIBase(%q) accepted invalid value", invalid)
		}
	}

	attachments := []struct {
		attachment providers.Attachment
		want       string
	}{
		{attachment: providers.Attachment{ContentType: " image/png "}, want: "image"},
		{attachment: providers.Attachment{Ref: "data:image/jpeg;base64,AA=="}, want: "image"},
		{attachment: providers.Attachment{URL: "data:image/webp;base64,AA=="}, want: "image"},
		{attachment: providers.Attachment{ContentType: "audio/ogg"}, want: "audio"},
		{attachment: providers.Attachment{ContentType: "video/mp4"}, want: "video"},
		{attachment: providers.Attachment{Filename: "photo.SVG"}, want: "image"},
		{attachment: providers.Attachment{Filename: "voice.opus"}, want: "audio"},
		{attachment: providers.Attachment{Filename: "clip.mkv"}, want: "video"},
		{attachment: providers.Attachment{Filename: "notes.txt"}, want: "file"},
	}
	for _, test := range attachments {
		if got := sessionAttachmentType(test.attachment); got != test.want {
			t.Fatalf("sessionAttachmentType(%#v) = %q, want %q", test.attachment, got, test.want)
		}
	}
}
