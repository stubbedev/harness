package config

import (
	"os"
)

func Init(workingDir, dataDir string, debug bool) (*ConfigStore, error) {
	store, err := Load(workingDir, dataDir, debug)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func HasInitialDataConfig(store *ConfigStore) bool {
	if store == nil {
		return false
	}
	cfgPath := GlobalConfigData()
	if _, err := os.Stat(cfgPath); err != nil {
		return false
	}
	return store.Config().IsConfigured()
}
