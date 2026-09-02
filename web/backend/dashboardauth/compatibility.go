package dashboardauth

// New preserves the original typed launcher-auth constructor. Local provider
// access still requires broker, migration, or test authority.
func New(dir string) (*Store, error) { return newLocal(dir) }

// NewWithLauncherConfig preserves the first-open legacy import constructor.
// Local provider access still requires broker, migration, or test authority.
func NewWithLauncherConfig(dir, launcherPath string) (*Store, error) {
	return newLocalWithLauncherConfig(dir, launcherPath)
}

// Open preserves the original typed launcher-auth constructor.
func Open(path string) (*Store, error) { return openLocal(path) }

// OpenWithLauncherConfig preserves the original typed import constructor.
func OpenWithLauncherConfig(path, launcherPath string) (*Store, error) {
	return openLocalWithLauncherConfig(path, launcherPath)
}
