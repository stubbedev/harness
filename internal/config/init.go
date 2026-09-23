package config

// Init loads the configuration for workingDir; see Load.
func Init(workingDir, dataDir string, debug bool) (*ConfigStore, error) {
	return Load(workingDir, dataDir, debug)
}
