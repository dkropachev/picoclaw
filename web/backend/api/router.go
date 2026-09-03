package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

// Handler serves HTTP API requests.
type Handler struct {
	configPath                                  string
	serverPort                                  int
	serverPublic                                bool
	serverPublicExplicit                        bool
	serverHostInput                             string
	serverHostExplicit                          bool
	serverCIDRs                                 []string
	serverAllowLocalhostBypass                  bool
	serverTrustedProxyCIDRs                     []string
	debug                                       bool
	embedGateway                                bool
	gatewayFatal                                func(error)
	oauthMu                                     sync.Mutex
	oauthFlows                                  map[string]*oauthFlow
	oauthState                                  map[string]string
	weixinMu                                    sync.Mutex
	weixinFlows                                 map[string]*weixinFlow
	wecomMu                                     sync.Mutex
	wecomFlows                                  map[string]*wecomFlow
	mcpMu                                       sync.Mutex
	mcpOAuthMu                                  sync.Mutex
	mcpOAuthFlows                               map[string]*mcpOAuthFlow
	mcpOAuthState                               map[string]string
	mcpOAuthLatestByServer                      map[string]string
	configMutationMu                            sync.Mutex
	prLifecycleEffectMu                         sync.Mutex
	prLifecyclePendingCatalog                   string
	prLifecycleAppliedDeferred                  string
	prLifecyclePendingDeferred                  string
	workflowDevelopmentMu                       sync.Mutex
	workflowTriggerReviewOnce                   sync.Once
	workflowTriggerReviewKey                    [32]byte
	workflowTriggerReviewErr                    error
	workflowTriggerReviewNow                    func() time.Time
	workflowTriggerReviewUseMu                  sync.Mutex
	workflowTriggerReviewUsed                   map[[32]byte]int64
	workflowDevelopmentTestDone                 func()
	repositoryReviewControllerMu                sync.Mutex
	repositoryReviewController                  *repositoryReviewController
	repositoryModelEvaluationControllerMu       sync.Mutex
	repositoryModelEvaluationController         *repositoryModelEvaluationController
	sessionReadAfterLookup                      func()
	sessionDeleteAfterLookup                    func()
	saveToolStateConfig                         func(string, *config.Config, string) (string, error)
	saveConfigIfRevision                        func(string, *config.Config, string) (string, error)
	projectAccountRouterItems                   accountRouterItemsProjector
	projectAccountRouterResource                accountRouterResourceProjector
	pageAccountRouters                          accountRouterPager
	validateAccountRouterCandidate              accountRouterCandidateValidator
	projectPRLifecycleRepositoryAssignmentItems prLifecycleRepositoryAssignmentProjector
	validatePRLifecycleCollectionCandidate      prLifecycleCollectionCandidateValidator
	savePRLifecycleCollectionCandidate          prLifecycleCollectionCandidateSaver
	loadGitWorkspaceManager                     func() (gitWorkspaceManagerAPI, error)
}

// NewHandler creates an instance of the API handler.
func NewHandler(configPath string) *Handler {
	return &Handler{
		configPath:                     configPath,
		serverPort:                     launcherconfig.DefaultPort,
		serverAllowLocalhostBypass:     launcherconfig.Default().AllowLocalhostBypass,
		oauthFlows:                     make(map[string]*oauthFlow),
		oauthState:                     make(map[string]string),
		weixinFlows:                    make(map[string]*weixinFlow),
		wecomFlows:                     make(map[string]*wecomFlow),
		mcpOAuthFlows:                  make(map[string]*mcpOAuthFlow),
		mcpOAuthState:                  make(map[string]string),
		mcpOAuthLatestByServer:         make(map[string]string),
		workflowTriggerReviewNow:       time.Now,
		workflowTriggerReviewUsed:      make(map[[32]byte]int64),
		saveToolStateConfig:            config.SaveConfigIfRevision,
		saveConfigIfRevision:           config.SaveConfigIfRevision,
		projectAccountRouterItems:      projectAccountRouterItems,
		projectAccountRouterResource:   accountRouterResourceForConfig,
		pageAccountRouters:             pageAccountRouters,
		validateAccountRouterCandidate: materializeAndValidateAccountRouterCandidate,
		projectPRLifecycleRepositoryAssignmentItems: projectPRLifecycleRepositoryAssignmentItems,
		validatePRLifecycleCollectionCandidate:      validatePRLifecycleWorkflowConfigurations,
	}
}

// SetServerOptions stores current backend listen options for fallback behavior.
func (h *Handler) SetServerOptions(port int, public bool, publicExplicit bool, allowedCIDRs []string) {
	h.serverPort = port
	h.serverPublic = public
	h.serverPublicExplicit = publicExplicit
	h.serverHostInput = ""
	h.serverHostExplicit = false
	h.serverCIDRs = append([]string(nil), allowedCIDRs...)
}

func (h *Handler) SetServerAccessOptions(allowLocalhostBypass bool, trustedProxyCIDRs []string) {
	h.serverAllowLocalhostBypass = allowLocalhostBypass
	h.serverTrustedProxyCIDRs = append([]string(nil), trustedProxyCIDRs...)
}

// SetServerBindHost stores the launcher's effective bind host.
// When explicit is true, hostInput is the normalized -host / PICOCLAW_LAUNCHER_HOST value.
func (h *Handler) SetServerBindHost(hostInput string, explicit bool) {
	h.serverHostInput = strings.TrimSpace(hostInput)
	if !explicit {
		h.serverHostInput = ""
	}
	h.serverHostExplicit = explicit
}

func (h *Handler) SetDebug(debug bool) {
	h.debug = debug
}

// EmbedGateway configures launcher gateway lifecycle operations to host the
// runtime inside this process instead of spawning a picoclaw child command.
func (h *Handler) EmbedGateway() {
	h.embedGateway = true
}

// SetGatewayFatalHandler installs the launcher-owned terminal failure sink for
// an embedded runtime that could not prove complete resource retirement.
func (h *Handler) SetGatewayFatalHandler(handler func(error)) {
	h.gatewayFatal = handler
}

// RegisterRoutes binds all API endpoint handlers to the ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Config CRUD
	h.registerConfigRoutes(mux)

	// Persistent agent management
	h.registerAgentRoutes(mux)

	// Pico Channel (WebSocket chat)
	h.registerPicoRoutes(mux)

	// Gateway process lifecycle
	h.registerGatewayRoutes(mux)

	// Session history
	h.registerSessionRoutes(mux)

	// Thread search and metadata
	h.registerThreadRoutes(mux)

	// OAuth login and credential management
	h.registerOAuthRoutes(mux)
	h.registerAccountRoutes(mux)

	// Model list management
	h.registerModelRoutes(mux)

	// Channel catalog (for frontend navigation/config pages)
	h.registerChannelRoutes(mux)

	// Skills and tools support/actions
	h.registerSkillRoutes(mux)
	h.registerToolRoutes(mux)
	h.registerMCPRoutes(mux)
	h.registerGitWorkspaceRoutes(mux)
	h.registerWorkflowRoutes(mux)
	h.registerEventRoutes(mux)
	h.registerPRWorkspaceRoutes(mux)
	h.registerRepositoryReviewRoutes(mux)
	h.registerRepositoryModelEvaluationRoutes(mux)
	h.registerPRLifecycleWorkflowConfigurationRoutes(mux)

	// OS startup / launch-at-login
	h.registerStartupRoutes(mux)

	// Launcher service parameters (port/public)
	h.registerLauncherConfigRoutes(mux)

	// Self-update endpoint (requires dashboard auth)
	h.registerUpdateRoutes(mux)

	// Runtime build/version metadata
	h.registerVersionRoutes(mux)

	// WeChat QR login flow
	h.registerWeixinRoutes(mux)

	// WeCom QR login flow
	h.registerWecomRoutes(mux)
}

// Shutdown gracefully shuts down the handler, stopping the gateway if it was started by this handler.
func (h *Handler) Shutdown() {
	h.beginEmbeddedGatewayShutdown()
	h.stopRepositoryModelEvaluationController()
	h.stopRepositoryReviewController()
	h.cancelMCPOAuthFlows()
	h.StopGateway()
}
