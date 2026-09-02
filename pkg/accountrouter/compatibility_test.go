package accountrouter

import "github.com/sipeed/picoclaw/pkg/config"

func New(
	name string,
	routerConfig *config.AccountRouterConfig,
	accounts map[string]Account,
	statePath string,
) *Router {
	return newRouter(name, routerConfig, accounts, statePath)
}

func NewSQLite(
	name string,
	routerConfig *config.AccountRouterConfig,
	accounts map[string]Account,
	statePath string,
) (*Router, error) {
	return newSQLiteRouter(name, routerConfig, accounts, statePath)
}
