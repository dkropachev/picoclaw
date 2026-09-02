package dashboardauth

func New(dir string) (*Store, error) { return newLocal(dir) }

func NewWithLauncherConfig(dir, launcherPath string) (*Store, error) {
	return newLocalWithLauncherConfig(dir, launcherPath)
}

func Open(path string) (*Store, error) { return openLocal(path) }

func OpenWithLauncherConfig(path, launcherPath string) (*Store, error) {
	return openLocalWithLauncherConfig(path, launcherPath)
}
