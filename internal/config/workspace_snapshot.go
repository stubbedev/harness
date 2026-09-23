package config

import "path/filepath"

// WorkspaceSnapshot returns a store for workingDir (e.g. a sub-agent's
// worktree) that starts from a copy of this store's config. Its
// workspace-scope state file lives in the data directory for workingDir,
// like any workspace's, never inside the project itself.
func (s *ConfigStore) WorkspaceSnapshot(workingDir string) *ConfigStore {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return &ConfigStore{
		config:         s.Config().cloneForWrite(),
		workingDir:     workingDir,
		resolver:       s.resolver,
		globalDataPath: s.globalDataPath,
		workspacePath:  filepath.Join(DefaultWorkspaceDataDirectory(workingDir), stateConfigFile),
	}
}
