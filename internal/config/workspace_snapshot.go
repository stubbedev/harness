package config

import "path/filepath"

func (s *ConfigStore) WorkspaceSnapshot(workingDir string) *ConfigStore {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return &ConfigStore{
		config:         s.Config().cloneForWrite(),
		workingDir:     workingDir,
		resolver:       s.resolver,
		globalDataPath: s.globalDataPath,
		workspacePath:  filepath.Join(workingDir, ".harness", "state.yaml"),
	}
}
