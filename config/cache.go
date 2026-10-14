package config

import "fmt"

type LocalCacheSettings struct {
	Enabled       bool     `json:"enabled"`
	TTLMinutes    int      `json:"ttlMinutes"`
	MaxHitPercent *float64 `json:"maxHitPercent,omitempty"`
}

func GetLocalCacheSettings() LocalCacheSettings {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	limit := 90.0
	settings := LocalCacheSettings{Enabled: true, TTLMinutes: 5, MaxHitPercent: &limit}
	if cfg != nil {
		if cfg.LocalCacheEnabled != nil {
			settings.Enabled = *cfg.LocalCacheEnabled
		}
		if cfg.LocalCacheTTLMinutes >= 1 && cfg.LocalCacheTTLMinutes <= 10080 {
			settings.TTLMinutes = cfg.LocalCacheTTLMinutes
		}
		if v := cfg.LocalCacheMaxHitPercent; v != nil && *v >= 0 && *v <= 100 {
			limit = *v
		}
	}
	return settings
}

func ValidateLocalCacheSettings(settings LocalCacheSettings) error {
	if settings.TTLMinutes < 1 || settings.TTLMinutes > 10080 {
		return fmt.Errorf("cache duration must be 1–10080 minutes")
	}
	if v := settings.MaxHitPercent; v != nil && !(*v >= 0 && *v <= 100) {
		return fmt.Errorf("maximum cache hit rate must be between 0 and 100 percent")
	}
	return nil
}

func UpdateLocalCacheSettings(settings LocalCacheSettings) error {
	if err := ValidateLocalCacheSettings(settings); err != nil {
		return err
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	oldEnabled, oldTTL, oldLimit := cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes, cfg.LocalCacheMaxHitPercent
	cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes = &settings.Enabled, settings.TTLMinutes
	if settings.MaxHitPercent != nil {
		limit := *settings.MaxHitPercent
		cfg.LocalCacheMaxHitPercent = &limit
	}
	if err := saveLocked(); err != nil {
		cfg.LocalCacheEnabled, cfg.LocalCacheTTLMinutes, cfg.LocalCacheMaxHitPercent = oldEnabled, oldTTL, oldLimit
		return err
	}
	return nil
}
