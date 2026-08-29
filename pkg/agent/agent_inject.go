// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
}

func (al *AgentLoop) GetRegistry() *AgentRegistry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.registry
}

func (al *AgentLoop) GetConfig() *config.Config {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cfg
}

// ExecutionPolicyForGeneration returns the immutable subprocess policy paired
// with expected. A stale or nil config identity fails closed.
func (al *AgentLoop) ExecutionPolicyForGeneration(
	expected *config.Config,
) (isolation.ExecutionPolicy, error) {
	executionPolicy, _, err := al.RuntimePoliciesForGeneration(expected)
	return executionPolicy, err
}

// RuntimePoliciesForGeneration returns the immutable owner policies paired
// with expected under one lock. It is a control-plane compatibility accessor;
// request code must use runtimeGenerationFromLease instead.
func (al *AgentLoop) RuntimePoliciesForGeneration(
	expected *config.Config,
) (isolation.ExecutionPolicy, logger.DiagnosticPolicy, error) {
	if al == nil || expected == nil {
		return isolation.ExecutionPolicy{}, logger.DiagnosticPolicy{},
			fmt.Errorf("runtime generation is not configured")
	}
	al.mu.RLock()
	defer al.mu.RUnlock()
	if al.cfg != expected {
		return isolation.ExecutionPolicy{}, logger.DiagnosticPolicy{},
			fmt.Errorf("runtime generation is stale")
	}
	return al.executionPolicy, al.diagnosticPolicy, nil
}

// SetMediaStore replaces the generation media store. The caller must hold the
// startup barrier or a paused/drained runtime boundary while tools can run.
func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	// Media-aware tools are mutable. Callers must use the startup barrier or a
	// paused/drained runtime generation before replacing a live store. This
	// mutex additionally serializes setters with each other and with reload's
	// candidate media apply/swap boundary.
	al.mediaStoreMu.Lock()
	defer al.mediaStoreMu.Unlock()

	al.mu.Lock()
	al.mediaStore = s
	registry := al.registry
	al.mu.Unlock()

	setAgentRegistryMediaStore(registry, s)
}

func (al *AgentLoop) mediaStoreSnapshot() media.MediaStore {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.mediaStore
}

func setAgentRegistryMediaStore(registry *AgentRegistry, store media.MediaStore) {
	if registry == nil {
		return
	}
	// Propagate store to all registered tools that can emit media.
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.SetMediaStore(store)
		}
	}
	registry.ForEachTool("send_tts", func(t tools.Tool) {
		if st, ok := t.(*tools.SendTTSTool); ok {
			st.SetMediaStore(store)
		}
	})
}

func (al *AgentLoop) SetTranscriber(t asr.Transcriber) {
	al.transcriber = t
}

func (al *AgentLoop) SetReloadFunc(fn func() error) {
	al.reloadFunc = fn
}

func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	registry := al.GetRegistry()
	agent := registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(registry.ListAgentIDs()),
		"ids":   registry.ListAgentIDs(),
	}

	return info
}
