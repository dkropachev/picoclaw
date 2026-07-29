package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowSettingsRequestMaxBytes = 1 << 20

type workflowSettingsPatchRequest struct {
	ExpectedConfigRevision *string `json:"expected_config_revision"`
	Enabled                *bool   `json:"enabled,omitempty"`
	DefinitionsDir         *string `json:"definitions_dir,omitempty"`
	MaxConcurrentRuns      *int    `json:"max_concurrent_runs,omitempty"`
	DefaultTimeoutSeconds  *int    `json:"default_timeout_seconds,omitempty"`
	MaxCallDepth           *int    `json:"max_call_depth,omitempty"`
	RetentionDays          *int    `json:"retention_days,omitempty"`
}

type workflowSettingsEffective struct {
	Enabled               bool   `json:"enabled"`
	DefinitionsDir        string `json:"definitions_dir"`
	MaxConcurrentRuns     int    `json:"max_concurrent_runs"`
	DefaultTimeoutSeconds int    `json:"default_timeout_seconds"`
	MaxCallDepth          int    `json:"max_call_depth"`
	RetentionDays         int    `json:"retention_days"`
}

type workflowSettingsEffects struct {
	LauncherEffect string `json:"launcher_effect"`
	CatalogEffect  string `json:"catalog_effect"`
	GatewayEffect  string `json:"gateway_effect"`
}

type workflowSettingsResponse struct {
	Configured     config.WorkflowsConfig    `json:"configured"`
	Effective      workflowSettingsEffective `json:"effective"`
	ConfigRevision string                    `json:"config_revision"`
	Effects        workflowSettingsEffects   `json:"effects"`
}

func (h *Handler) handleGetWorkflowSettings(w http.ResponseWriter, _ *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := loadStableWorkflowSettingsConfig(h.configPath)
	if err != nil {
		http.Error(w, "workflow settings are unavailable", http.StatusInternalServerError)
		return
	}
	writeWorkflowSettingsResponse(
		w,
		cfg.Workflows,
		revision,
		workflowSettingsEffects{
			LauncherEffect: "applied",
			CatalogEffect:  "applied",
			GatewayEffect:  h.workflowSettingsGatewayEffect(),
		},
	)
}

func (h *Handler) handlePatchWorkflowSettings(w http.ResponseWriter, r *http.Request) {
	var request workflowSettingsPatchRequest
	if err := decodeWorkflowSettingsPatch(w, r, &request); err != nil {
		if maxBytesErr := new(http.MaxBytesError); errors.As(err, &maxBytesErr) {
			http.Error(w, "workflow settings request exceeds 1 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid workflow settings request", http.StatusBadRequest)
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		http.Error(w, "expected_config_revision is required", http.StatusBadRequest)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, currentRevision, err := loadStableWorkflowSettingsConfig(h.configPath)
	if err != nil {
		http.Error(w, "workflow settings are unavailable", http.StatusInternalServerError)
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "config_revision_mismatch",
		})
		return
	}
	previous := cfg.Workflows
	next := previous
	applyWorkflowSettingsPatch(&next, request)
	if err := validateWorkflowSettings(cfg.WorkspacePath(), next); err != nil {
		writeWorkflowJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "invalid_workflow_settings",
		})
		return
	}
	definitionsChanged := previous.EffectiveDefinitionsDir() !=
		next.EffectiveDefinitionsDir()
	if definitionsChanged {
		if !h.workflowDevelopmentMu.TryLock() {
			writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
				"error": "workflow_development_busy",
			})
			return
		}
		defer h.workflowDevelopmentMu.Unlock()
	}
	latestRevision, err := workflowConfigFileRevision(h.configPath)
	if err != nil {
		http.Error(w, "workflow settings are unavailable", http.StatusInternalServerError)
		return
	}
	if latestRevision != currentRevision {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "config_revision_mismatch",
		})
		return
	}
	cfg.Workflows = next
	var revision string
	save := func() error {
		var saveErr error
		revision, saveErr = config.SaveConfigIfRevision(
			h.configPath,
			cfg,
			currentRevision,
		)
		return saveErr
	}
	if definitionsChanged {
		err = workflows.WithWorkflowMutationLockAndDevelopmentSession(
			cfg.WorkspacePath(),
			func(active *workflows.WorkflowDevelopmentSession) error {
				if active != nil {
					return workflows.ErrActiveDevelopmentExists
				}
				return save()
			},
		)
	} else {
		err = save()
	}
	if errors.Is(err, workflows.ErrActiveDevelopmentExists) {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "workflow_development_active",
		})
		return
	}
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "config_revision_mismatch",
		})
		return
	}
	if err != nil {
		http.Error(w, "failed to save workflow settings", http.StatusInternalServerError)
		return
	}
	catalogEffect := "applied"
	if previous.Enabled != next.Enabled ||
		previous.EffectiveDefinitionsDir() != next.EffectiveDefinitionsDir() {
		catalogEffect = "reload_required"
	}
	writeWorkflowSettingsResponse(
		w,
		next,
		revision,
		workflowSettingsEffects{
			LauncherEffect: "applied",
			CatalogEffect:  catalogEffect,
			GatewayEffect:  h.workflowSettingsGatewayEffect(),
		},
	)
}

func decodeWorkflowSettingsPatch(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflowSettingsPatchRequest,
) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowSettingsRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow settings request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func applyWorkflowSettingsPatch(
	destination *config.WorkflowsConfig,
	request workflowSettingsPatchRequest,
) {
	if request.Enabled != nil {
		destination.Enabled = *request.Enabled
	}
	if request.DefinitionsDir != nil {
		destination.DefinitionsDir = strings.TrimSpace(*request.DefinitionsDir)
	}
	if request.MaxConcurrentRuns != nil {
		destination.MaxConcurrentRuns = *request.MaxConcurrentRuns
	}
	if request.DefaultTimeoutSeconds != nil {
		destination.DefaultTimeoutSeconds = *request.DefaultTimeoutSeconds
	}
	if request.MaxCallDepth != nil {
		destination.MaxCallDepth = *request.MaxCallDepth
	}
	if request.RetentionDays != nil {
		destination.RetentionDays = *request.RetentionDays
	}
}

func validateWorkflowSettings(workspace string, settings config.WorkflowsConfig) error {
	if settings.MaxConcurrentRuns < 0 ||
		settings.DefaultTimeoutSeconds < 0 ||
		settings.MaxCallDepth < 0 ||
		settings.RetentionDays < 0 {
		return errors.New("workflow numeric settings must be non-negative")
	}
	if settings.MaxConcurrentRuns > config.MaxWorkflowMaxConcurrentRuns ||
		settings.DefaultTimeoutSeconds > config.MaxWorkflowDefaultTimeoutSeconds ||
		settings.MaxCallDepth > config.MaxWorkflowMaxCallDepth ||
		settings.RetentionDays > config.MaxWorkflowRetentionDays {
		return errors.New("workflow numeric settings exceed supported limits")
	}
	_, err := (workflows.Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: settings.EffectiveDefinitionsDir(),
	}).ResolveLocal("workflows/settings-check.yml")
	return err
}

func writeWorkflowSettingsResponse(
	w http.ResponseWriter,
	settings config.WorkflowsConfig,
	revision string,
	effects workflowSettingsEffects,
) {
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSON(w, workflowSettingsResponse{
		Configured: settings,
		Effective: workflowSettingsEffective{
			Enabled:               settings.Enabled,
			DefinitionsDir:        settings.EffectiveDefinitionsDir(),
			MaxConcurrentRuns:     settings.EffectiveMaxConcurrentRuns(),
			DefaultTimeoutSeconds: int(settings.EffectiveDefaultTimeout().Seconds()),
			MaxCallDepth:          settings.EffectiveMaxCallDepth(),
			RetentionDays:         settings.EffectiveRetentionDays(),
		},
		ConfigRevision: revision,
		Effects:        effects,
	})
}

func (h *Handler) workflowSettingsGatewayEffect() string {
	status := h.gatewayStatusData()
	if restartRequired, _ := status["gateway_restart_required"].(bool); restartRequired {
		return "restart_required"
	}
	return "applied"
}

func workflowConfigFileRevision(path string) (string, error) {
	return config.ConfigRevision(path)
}

func loadStableWorkflowSettingsConfig(
	path string,
) (*config.Config, string, error) {
	return config.LoadConfigForUpdateSnapshot(path)
}
