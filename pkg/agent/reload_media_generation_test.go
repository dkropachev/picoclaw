package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type reloadMediaStoreProbe struct {
	id    string
	calls atomic.Int64
}

type reloadMediaSetterBlock struct {
	first   media.MediaStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type reloadMediaSetterPanic struct{}

func (*reloadMediaSetterPanic) Name() string { return "reload_media_setter_panic" }
func (*reloadMediaSetterPanic) Description() string {
	return "reload media setter panic probe"
}

func (*reloadMediaSetterPanic) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*reloadMediaSetterPanic) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.SilentResult("ok")
}

func (*reloadMediaSetterPanic) SetMediaStore(media.MediaStore) { panic("candidate media panic") }

func (*reloadMediaSetterBlock) Name() string { return "reload_media_setter_block" }
func (*reloadMediaSetterBlock) Description() string {
	return "reload media setter ordering probe"
}

func (*reloadMediaSetterBlock) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*reloadMediaSetterBlock) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.SilentResult("ok")
}

func (tool *reloadMediaSetterBlock) SetMediaStore(store media.MediaStore) {
	if store != tool.first {
		return
	}
	tool.once.Do(func() { close(tool.started) })
	<-tool.release
}

func (store *reloadMediaStoreProbe) Store(
	string,
	media.MediaMeta,
	string,
) (string, error) {
	store.calls.Add(1)
	return "media://" + store.id, nil
}

func (*reloadMediaStoreProbe) Resolve(string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (*reloadMediaStoreProbe) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, fmt.Errorf("not implemented")
}

func (*reloadMediaStoreProbe) ReleaseAll(string) error { return nil }

func TestReloadPublishesCandidateWithLatestRetainedMediaStore(t *testing.T) {
	cfgA := config.DefaultConfig()
	cfgA.Agents.Defaults.Workspace = t.TempDir()
	cfgA.Agents.Defaults.ModelName = "gpt-5"
	cfgA.Agents.Defaults.Provider = "openai"
	providerA := &mockProvider{}
	loop := newTestAgentLoopWithStrictModels(cfgA, bus.NewMessageBus(), providerA)
	defer loop.Close()

	oldStore := &reloadMediaStoreProbe{id: "old"}
	latestStore := &reloadMediaStoreProbe{id: "latest"}
	loop.SetMediaStore(oldStore)
	oldRegistry := loop.GetRegistry()

	cfgB := config.DefaultConfig()
	cfgB.Agents.Defaults.Workspace = t.TempDir()
	cfgB.Agents.Defaults.ModelName = "gpt-5"
	cfgB.Agents.Defaults.Provider = "openai"
	providerB := &mockProvider{}
	ensureStrictTestModelSelection(cfgB, providerB)
	candidateReady := make(chan struct{})
	candidateMedia := &reloadMediaSetterBlock{
		first: oldStore, started: make(chan struct{}), release: make(chan struct{}),
	}
	loop.registryFactory = func(
		gotConfig *config.Config,
		gotProvider providers.LLMProvider,
	) *AgentRegistry {
		if gotConfig != cfgB || gotProvider != providerB {
			panic("reload registry factory received the wrong generation")
		}
		candidate := NewAgentRegistry(gotConfig, gotProvider)
		candidate.GetDefaultAgent().Tools.Register(candidateMedia)
		close(candidateReady)
		return candidate
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loop.ReloadProviderAndConfig(context.Background(), providerB, cfgB)
	}()
	select {
	case <-candidateReady:
	case <-time.After(5 * time.Second):
		t.Fatal("reload candidate construction did not start")
	}
	select {
	case <-candidateMedia.started:
	case <-time.After(5 * time.Second):
		t.Fatal("reload candidate media application did not start")
	}
	latestStarted := make(chan struct{})
	latestDone := make(chan struct{})
	go func() {
		close(latestStarted)
		loop.SetMediaStore(latestStore)
		close(latestDone)
	}()
	<-latestStarted
	select {
	case <-latestDone:
		t.Fatal("media setter crossed an in-progress candidate apply/swap")
	case <-time.After(50 * time.Millisecond):
	}
	close(candidateMedia.release)
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not finish")
	}
	select {
	case <-latestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("latest media setter did not finish after reload publication")
	}

	currentRegistry := loop.GetRegistry()
	if currentRegistry == oldRegistry {
		t.Fatal("reload retained the old registry")
	}
	loadImage, ok := currentRegistry.GetDefaultAgent().Tools.Get("load_image")
	if !ok {
		t.Fatal("reloaded load_image tool is unavailable")
	}
	imagePath := filepath.Join(cfgB.Agents.Defaults.Workspace, "reload.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	if err := os.WriteFile(imagePath, pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithToolContext(context.Background(), "test", "reload-media")
	result := loadImage.Execute(ctx, map[string]any{"path": imagePath})
	if result == nil || result.IsError || len(result.Media) != 1 ||
		!strings.HasPrefix(result.Media[0], "media://latest") {
		t.Fatalf("reloaded load_image result = %#v", result)
	}
	if latestStore.calls.Load() != 1 || oldStore.calls.Load() != 0 {
		t.Fatalf(
			"media stores called after reload = latest:%d old:%d",
			latestStore.calls.Load(),
			oldStore.calls.Load(),
		)
	}
}

func TestConcurrentSetMediaStoreSerializesLatestStore(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.Defaults.Provider = "openai"
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()
	firstStore := &reloadMediaStoreProbe{id: "first"}
	latestStore := &reloadMediaStoreProbe{id: "latest"}
	blocker := &reloadMediaSetterBlock{
		first: firstStore, started: make(chan struct{}), release: make(chan struct{}),
	}
	loop.GetRegistry().GetDefaultAgent().Tools.Register(blocker)

	firstDone := make(chan struct{})
	go func() {
		loop.SetMediaStore(firstStore)
		close(firstDone)
	}()
	select {
	case <-blocker.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first media-store application did not block")
	}
	latestStarted := make(chan struct{})
	latestDone := make(chan struct{})
	go func() {
		close(latestStarted)
		loop.SetMediaStore(latestStore)
		close(latestDone)
	}()
	<-latestStarted
	select {
	case <-latestDone:
		t.Fatal("newer media setter overtook the blocked earlier setter")
	case <-time.After(50 * time.Millisecond):
	}
	close(blocker.release)
	for label, done := range map[string]<-chan struct{}{
		"first": firstDone, "latest": latestDone,
	} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s SetMediaStore did not converge", label)
		}
	}

	loadImage, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("load_image")
	if !ok {
		t.Fatal("load_image tool is unavailable")
	}
	imagePath := filepath.Join(cfg.Agents.Defaults.Workspace, "latest.png")
	if err := os.WriteFile(imagePath, []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithToolContext(context.Background(), "test", "latest-media")
	result := loadImage.Execute(ctx, map[string]any{"path": imagePath})
	if result == nil || result.IsError || len(result.Media) != 1 ||
		result.Media[0] != "media://latest" {
		t.Fatalf("load_image used a stale concurrent media store: %#v", result)
	}
}

func TestReloadMediaPanicClosesCandidateEvolutionAndRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.Defaults.Provider = "openai"
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()
	loop.SetMediaStore(&reloadMediaStoreProbe{id: "installed"})
	currentRegistry := loop.GetRegistry()
	baselineSubscribers := loop.RuntimeEventStats().Subscribers
	var candidate *AgentRegistry
	loop.registryFactory = func(
		gotConfig *config.Config,
		gotProvider providers.LLMProvider,
	) *AgentRegistry {
		candidate = NewAgentRegistry(gotConfig, gotProvider)
		candidate.GetDefaultAgent().Tools.Register(&reloadMediaSetterPanic{})
		return candidate
	}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, cfg)
	}()
	if recovered != "candidate media panic" {
		t.Fatalf("reload panic = %#v, want candidate media panic", recovered)
	}
	if loop.GetRegistry() != currentRegistry {
		t.Fatal("panicking candidate replaced the current registry")
	}
	if candidate == nil || candidate.GetDefaultAgent().Tools.Count() != 0 {
		t.Fatal("panicking candidate registry was not closed")
	}
	if got := loop.RuntimeEventStats().Subscribers; got != baselineSubscribers {
		t.Fatalf("runtime-event subscribers after candidate panic = %d, want %d", got, baselineSubscribers)
	}
}

func TestSetAgentRegistryMediaStoreAllowsNilRegistry(t *testing.T) {
	setAgentRegistryMediaStore(nil, &reloadMediaStoreProbe{id: "unused"})
}
