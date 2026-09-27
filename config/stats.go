package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// All revisions are protected by cfgLock. Usage can change during a disk write;
// settings saves/reloads invalidate that write, while new usage stays dirty.
var configRevision, statsRevision, savedStatsRevision uint64

// UpdateStats saves global, account and API-key usage together, only if changed.
// Copy under the lock; serialize and write the temporary file without it.
func UpdateStats(totalReq, successReq, failedReq, totalTokens int, totalCredits float64) error {
	cfgLock.Lock()
	if cfg.TotalRequests != totalReq || cfg.SuccessRequests != successReq ||
		cfg.FailedRequests != failedReq || cfg.TotalTokens != totalTokens || cfg.TotalCredits != totalCredits {
		cfg.TotalRequests, cfg.SuccessRequests, cfg.FailedRequests = totalReq, successReq, failedReq
		cfg.TotalTokens, cfg.TotalCredits = totalTokens, totalCredits
		statsRevision++
	}
	if statsRevision == savedStatsRevision {
		cfgLock.Unlock()
		return nil
	}
	snapshot := *cfg
	snapshot.Accounts = append([]Account{}, cfg.Accounts...)
	snapshot.ModelCooldowns = append([]ModelCooldown(nil), cfg.ModelCooldowns...)
	snapshot.ApiKeys = append([]ApiKeyEntry(nil), cfg.ApiKeys...)
	snapshot.PromptFilterRules = append([]PromptFilterRule(nil), cfg.PromptFilterRules...)
	path, revision, usageRevision := cfgPath, configRevision, statsRevision
	cfgLock.Unlock()

	data, err := json.MarshalIndent(&snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := writeConfigTemp(path, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return commitStatsSnapshot(tmp, path, revision, usageRevision)
}

// Model cooldowns share the existing batched save, never writing on the request path.
func GetModelCooldowns() []ModelCooldown {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	return append([]ModelCooldown(nil), cfg.ModelCooldowns...)
}

func SetModelCooldown(id, model string, until time.Time) {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return
	}
	now := time.Now().Unix()
	entries := make([]ModelCooldown, 0, len(cfg.ModelCooldowns)+1)
	for _, entry := range cfg.ModelCooldowns {
		if entry.Until > now && (entry.AccountID != id || entry.Model != model) {
			entries = append(entries, entry)
		}
	}
	entries = append(entries, ModelCooldown{AccountID: id, Model: model, Until: until.Unix()})
	cfg.ModelCooldowns = entries
	statsRevision++
}

// Persist manual resets immediately so a restart cannot restore old cooldowns.
func ResetModelCooldowns() error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	previous := cfg.ModelCooldowns
	cfg.ModelCooldowns = nil
	if err := saveLocked(); err != nil {
		cfg.ModelCooldowns = previous
		return err
	}
	return nil
}

func commitStatsSnapshot(tmp, path string, revision, usageRevision uint64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if revision != configRevision {
		return nil // A newer save/reload wins. Dirty usage is retried next tick.
	}
	if err := replaceConfigFile(tmp, path); err != nil {
		return err
	}
	configRevision++
	savedStatsRevision = usageRevision
	return nil
}

func replaceConfigFile(tmp, path string) error {
	err := os.Rename(tmp, path)
	// Windows can briefly deny replacement while another reader has the file
	// open. Keep replacement atomic; never fall back to truncating the config.
	for attempt := 0; runtime.GOOS == "windows" && os.IsPermission(err) && attempt < 3; attempt++ {
		time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
		err = os.Rename(tmp, path)
	}
	return err
}

// Atomic replacement prevents a shutdown during writing from truncating config.
func writeConfigTemp(path string, data []byte) (name string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return "", err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
