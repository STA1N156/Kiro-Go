package config

import "fmt"

type LocalCacheSettings struct {
	Enabled    bool `json:"enabled"`
	TTLMinutes int  `json:"ttlMinutes"`
}

func GetLocalCacheSettings() LocalCacheSettings {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	settings := LocalCacheSettings{Enabled: true, TTLMinutes: 5}
	if cfg != nil {
		if cfg.LocalCacheEnabled != nil {
			settings.Enabled = *cfg.LocalCacheEnabled
		}
		if cfg.LocalCacheTTLMinutes >= 1 && cfg.LocalCacheTTLMinutes <= 10080 {
			settings.TTLMinutes = cfg.LocalCacheTTLMinutes
		}
	}
	return settings
}

func UpdateLocalCacheSettings(settings LocalCacheSettings) error {
	if settings.TTLMinutes < 1 || settings.TTLMinutes > 10080 {
		return fmt.Errorf("cache duration must be 1–10080 minutes")
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	oldEnabled, oldTTL := cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes
	cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes = &settings.Enabled, settings.TTLMinutes
	if err := saveLocked(); err != nil {
		cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes = oldEnabled, oldTTL
		return err
	}
	return nil
}
