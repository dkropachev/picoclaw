package tools

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestToolAdaptationClientPaginationAndFailureMatrix(t *testing.T) {
	home := t.TempDir()
	mode := "success"
	pageCall := 0
	revisionA := strings.Repeat("a", 64)
	revisionB := strings.Repeat("b", 64)
	outcome := ToolAdaptationToolOutcome{
		Profile:            ToolAdaptationProfile{Provider: "openai", Model: "model"},
		VisibleToolSurface: "pico", ToolName: "shell", Successes: 1,
	}
	handler := database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
		switch request.Operation {
		case "observe-cache":
			if mode == "error" {
				return nil, database.NewError(database.CodeUnavailable, "down")
			}
			return adaptationObservationResponse{Found: true, Observation: ToolAdaptationObservation{
				Profile: outcome.Profile, ToolSchemaHash: "hash",
			}}, nil
		case "latest-observation":
			if mode == "error" {
				return nil, database.NewError(database.CodeUnavailable, "down")
			}
			return adaptationObservationResponse{Found: true, Observation: ToolAdaptationObservation{
				Profile: outcome.Profile, ToolSchemaHash: "hash",
			}}, nil
		case "observe-outcome":
			if mode == "error" {
				return nil, database.NewError(database.CodeUnavailable, "down")
			}
			return adaptationOutcomeResponse{Found: true, Outcome: outcome}, nil
		case "latest-outcomes-page":
			pageCall++
			switch mode {
			case "error":
				return nil, database.NewError(database.CodeUnavailable, "down")
			case "invalid-revision":
				return adaptationOutcomesResponse{Done: true, Revision: "BAD"}, nil
			case "too-many":
				return adaptationOutcomesResponse{
					Outcomes: make([]ToolAdaptationToolOutcome, adaptationPageItems+1),
					Next:     adaptationPageItems + 1, Revision: revisionA, Done: true,
				}, nil
			case "no-progress":
				return adaptationOutcomesResponse{
					Outcomes: []ToolAdaptationToolOutcome{outcome}, Revision: revisionA,
				}, nil
			case "empty-progress":
				return adaptationOutcomesResponse{Next: 1, Revision: revisionA}, nil
			case "mismatch":
				if pageCall == 1 {
					return adaptationOutcomesResponse{
						Outcomes: []ToolAdaptationToolOutcome{outcome}, Next: 1, Revision: revisionA,
					}, nil
				}
				return adaptationOutcomesResponse{Done: true, Next: 1, Revision: revisionB}, nil
			case "backward":
				if pageCall == 1 {
					return adaptationOutcomesResponse{
						Outcomes: []ToolAdaptationToolOutcome{outcome}, Next: 1, Revision: revisionA,
					}, nil
				}
				return adaptationOutcomesResponse{Done: true, Next: 0, Revision: revisionA}, nil
			case "multi":
				if pageCall == 1 {
					return adaptationOutcomesResponse{
						Outcomes: []ToolAdaptationToolOutcome{outcome}, Next: 1, Revision: revisionA,
					}, nil
				}
				second := outcome
				second.ToolName = "read"
				return adaptationOutcomesResponse{
					Outcomes: []ToolAdaptationToolOutcome{second}, Next: 2, Revision: revisionA, Done: true,
				}, nil
			default:
				return adaptationOutcomesResponse{
					Outcomes: []ToolAdaptationToolOutcome{outcome}, Next: 1, Revision: revisionA, Done: true,
				}, nil
			}
		default:
			return nil, database.NewError(database.CodeUnsupported, "unsupported")
		}
	})
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() {
		database.InstallProcessClient(previousClient)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	profile := outcome.Profile
	if observation, ok := ObserveToolAdaptationCache(
		profile, "pico", nil, &providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens},
	); !ok || observation.ToolSchemaHash != "hash" {
		t.Fatalf("broker cache observation = %#v/%v", observation, ok)
	}
	if observation, ok := LatestToolAdaptationObservation(profile); !ok || observation.ToolSchemaHash != "hash" {
		t.Fatalf("broker latest observation = %#v/%v", observation, ok)
	}
	if value, ok := ObserveToolAdaptationToolOutcome(
		profile,
		"pico",
		"shell",
		true,
		"",
		time.Second,
	); !ok ||
		value.ToolName != "shell" {
		t.Fatalf("broker outcome = %#v/%v", value, ok)
	}
	if values := LatestToolAdaptationToolOutcomes(profile); len(values) != 1 {
		t.Fatalf("single outcome page = %#v", values)
	}
	mode = "error"
	if _, ok := ObserveToolAdaptationCache(profile, "pico", nil, nil); ok {
		t.Fatal("failed cache broker call reported success")
	}
	if _, ok := LatestToolAdaptationObservation(profile); ok {
		t.Fatal("failed latest broker call reported success")
	}
	if _, ok := ObserveToolAdaptationToolOutcome(profile, "pico", "shell", false, "failed", 0); ok {
		t.Fatal("failed outcome broker call reported success")
	}
	if values := LatestToolAdaptationToolOutcomes(profile); values != nil {
		t.Fatalf("failed outcome page = %#v", values)
	}
	for _, testMode := range []string{
		"invalid-revision", "too-many", "no-progress", "empty-progress", "mismatch", "backward",
	} {
		mode, pageCall = testMode, 0
		if values := LatestToolAdaptationToolOutcomes(profile); values != nil {
			t.Errorf("%s page = %#v", testMode, values)
		}
	}
	mode, pageCall = "multi", 0
	if values := LatestToolAdaptationToolOutcomes(profile); len(values) != 2 || values[1].ToolName != "read" {
		t.Fatalf("multi-page outcomes = %#v", values)
	}
}

func TestToolAdaptationAuthorityMigrationAndIdentifierResiduals(t *testing.T) {
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })
	restoreAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedAdaptationProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedAdaptationProviderForTests.Store(true)
		restoreAuthority()
	})
	profile := ToolAdaptationProfile{Provider: "openai", Model: "model"}
	if _, ok := ObserveToolAdaptationCache(profile, "pico", nil, nil); ok {
		t.Fatal("unfenced cache observation succeeded")
	}
	if _, ok := LatestToolAdaptationObservation(profile); ok {
		t.Fatal("unfenced latest observation succeeded")
	}
	if _, ok := ObserveToolAdaptationToolOutcome(profile, "pico", "shell", true, "", 0); ok {
		t.Fatal("unfenced outcome succeeded")
	}
	if values := LatestToolAdaptationToolOutcomes(profile); values != nil {
		t.Fatalf("unfenced outcomes = %#v", values)
	}
	if _, err := (&toolAdaptationStateStore{}).openLocked(
		t.Context(),
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("unfenced adaptation open error = %v", err)
	}
	allowUnfencedAdaptationProviderForTests.Store(true)
	restoreAuthority()

	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	if err := RunOfflineDatabaseMigration(t.Context(), home); err != nil {
		t.Fatalf("offline adaptation migration = %v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]string{
		"___": "unnamed", strings.Repeat("A", 80): strings.Repeat("a", 64), "A  B__C": "a_b_c",
	} {
		if got := sanitizeIdentifierComponent(input); got != want {
			t.Errorf("sanitizeIdentifierComponent(%q) = %q, want %q", input, got, want)
		}
	}
	var nilStore *toolAdaptationStateStore
	nilStore.clearError()
	if nilStore.consumeError() != nil {
		t.Fatal("nil adaptation store consumed an error")
	}
	if err := nilStore.close(); err != nil {
		t.Fatalf("nil adaptation close = %v", err)
	}
	if err := (&toolAdaptationStateStore{}).close(); err != nil {
		t.Fatalf("empty adaptation close = %v", err)
	}
	persistentDB := &sql.DB{}
	persistent := &toolAdaptationStateStore{persistent: true, database: persistentDB}
	if opened, err := persistent.openLocked(t.Context()); err != nil || opened != persistentDB {
		t.Fatalf("retained adaptation database = %#v, %v", opened, err)
	}
	warnAdaptationBroker("coverage warning", context.Canceled)
}

func TestToolInlineMediaStorageFailureResiduals(t *testing.T) {
	dataURL := "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("payload"))
	t.Run("mkdir", func(t *testing.T) {
		blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", blockingFile)
		ref, note := storeInlineDataURL(
			"tool", &closeoutMediaStore{}, "channel", "chat", dataURL, map[string]struct{}{},
		)
		if ref != "" || !strings.Contains(note, "could not be stored") {
			t.Fatalf("mkdir failure = %q, %q", ref, note)
		}
	})
	t.Run("create", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv("TMPDIR", tempRoot)
		mediaDir := media.TempDir()
		if err := os.MkdirAll(mediaDir, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(mediaDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(mediaDir, 0o700) })
		ref, note := storeInlineDataURL(
			"tool", &closeoutMediaStore{}, "channel", "chat", dataURL, map[string]struct{}{},
		)
		if ref != "" || !strings.Contains(note, "could not be stored") {
			t.Fatalf("create failure = %q, %q", ref, note)
		}
	})
}
