//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGatewayEventAutomationPureResidualMatrix(t *testing.T) {
	if _, err := defaultPRWorkspaceRuntimeAgent(nil); err == nil {
		t.Fatal("nil PR workspace runtime accepted")
	}
	if githubReviewSubmissionReady(t.Context(), nil) || githubReviewProviderReadReady(t.Context(), nil) ||
		githubDevelopmentPullCreationReady(t.Context(), nil) ||
		githubDevelopmentProviderReadReady(t.Context(), nil) {
		t.Fatal("nil agent loop reported GitHub readiness")
	}
	configureGitHubMCPReviewRuntime(nil, nil, "artifact", true, true)
	if githubNotificationPollingEnabled(nil) {
		t.Fatal("nil config enabled notification polling")
	}
	cfg := eventAutomationTestConfig(t.TempDir(), "", true, false)
	if githubNotificationPollingEnabled(cfg) {
		t.Fatal("config without polling enabled notification polling")
	}
	cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"disabled": {
			Enabled: false, Format: config.EventWebhookFormatGitHub, PollNotifications: true,
		},
		"standard": {
			Enabled: true, Format: config.EventWebhookFormatStandard, PollNotifications: true,
		},
	}
	if githubNotificationPollingEnabled(cfg) {
		t.Fatal("ineligible connectors enabled notification polling")
	}
	cfg.Events.Ingress.Webhooks["github"] = config.GenericWebhookConfig{
		Enabled: true, Format: config.EventWebhookFormatGitHub, PollNotifications: true,
	}
	if !githubNotificationPollingEnabled(cfg) {
		t.Fatal("GitHub poll connector was not enabled")
	}
	if err := validateGitHubNotificationPollingRuntime(nil, cfg, nil); err == nil {
		t.Fatal("polling without agent runtime succeeded")
	}
	if _, err := newGitHubNotificationPoller(cfg, nil, nil, ""); err == nil {
		t.Fatal("poller without store succeeded")
	}
	if _, err := newGitHubNotificationPoller(cfg, gatewayDiscardNotificationInserter{}, nil, ""); err == nil {
		t.Fatal("poller without tool runner succeeded")
	}
	for _, test := range []struct {
		now       time.Time
		retention int
		valid     bool
		wantErr   bool
	}{
		{time.Now().UTC(), 0, false, true},
		{time.Time{}, 1, false, true},
		{time.Now().UTC(), eventRetentionMaxDurableDays + 1, false, false},
		{time.Now().UTC(), 1, true, false},
	} {
		_, valid, err := durableEventRetentionCutoff(test.now, test.retention)
		if valid != test.valid || (err != nil) != test.wantErr {
			t.Errorf("retention cutoff %#v = %v/%v", test, valid, err)
		}
	}
	if wrapped := withEventAutomationRuntime(nil, func(context.Context) (bool, error) {
		return true, nil
	}); wrapped == nil {
		t.Fatal("nil-acquire worker wrapper was nil")
	}
}
