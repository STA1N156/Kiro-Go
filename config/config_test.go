package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResetModelCooldownsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	SetModelCooldown("account", "claude-opus-5.5", time.Now().Add(time.Hour))
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := writeConfigTemp(path, data)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	revision, usageRevision := configRevision, statsRevision
	if err := ResetModelCooldowns(); err != nil {
		t.Fatal(err)
	}
	if err := commitStatsSnapshot(tmp, path, revision, usageRevision); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if len(GetModelCooldowns()) != 0 {
		t.Fatal("old snapshot or reload restored cleared cooldowns")
	}
}

func TestResetModelCooldownsSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	SetModelCooldown("account", "claude-opus-5.5", until)
	cfgPath = filepath.Join(t.TempDir(), "missing", "config.json")
	t.Cleanup(func() { cfgPath = path })
	if err := ResetModelCooldowns(); err == nil {
		t.Fatal("expected save failure")
	}
	if entries := GetModelCooldowns(); len(entries) != 1 || entries[0].Until != until.Unix() {
		t.Fatal("failed reset changed cooldowns")
	}
}

func TestUsageBatchedPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	key, err := AddApiKey(ApiKeyEntry{Key: "sk-batched", Enabled: true, TokenLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddAccount(Account{ID: "account", AccessToken: "fake", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	staleAccount := GetAccounts()[0]
	if err := RecordApiKeyUsage(key.ID, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccountStats("account", 1, 0, 100, 1, 123); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("per-request usage wrote to disk")
	}
	if over, _ := ApiKeyOverLimit(*GetApiKeyEntry(key.ID)); !over {
		t.Fatal("quota must update before disk flush")
	}
	if err := UpdateStats(1, 1, 0, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if got := GetApiKeyEntry(key.ID); got.TokensUsed != 100 || got.RequestsCount != 1 {
		t.Fatalf("key usage lost: %+v", got)
	}
	if got := GetAccounts()[0]; got.TotalTokens != 100 || got.RequestCount != 1 {
		t.Fatalf("account usage lost: %+v", got)
	}
	if n, _, _, tokens, _ := GetStats(); n != 1 || tokens != 100 {
		t.Fatal("global usage lost")
	}
	revision := configRevision
	if err := UpdateStats(1, 1, 0, 100, 1); err != nil {
		t.Fatal(err)
	}
	if configRevision != revision {
		t.Fatal("idle flush rewrote config")
	}
	staleAccount.Nickname = "renamed"
	if err := UpdateAccount("account", staleAccount); err != nil {
		t.Fatal(err)
	}
	if GetAccounts()[0].TotalTokens != 100 {
		t.Fatal("administrative snapshot rolled back live usage")
	}
}

func TestUsageSnapshotPreservesNewUsageAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	key, err := AddApiKey(ApiKeyEntry{Key: "sk-snapshot", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordApiKeyUsage(key.ID, 100, 1); err != nil {
		t.Fatal(err)
	}
	makeSnapshot := func() (string, uint64, uint64) {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		tmp, err := writeConfigTemp(path, data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(tmp) })
		return tmp, configRevision, statsRevision
	}
	tmp, revision, usageRevision := makeSnapshot()
	if err := RecordApiKeyUsage(key.ID, 200, 2); err != nil {
		t.Fatal(err)
	}
	if err := commitStatsSnapshot(tmp, path, revision, usageRevision); err != nil {
		t.Fatal(err)
	}
	if savedStatsRevision == statsRevision {
		t.Fatal("usage arriving during disk write was marked saved")
	}
	if err := UpdateStats(2, 2, 0, 300, 3); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if GetApiKeyEntry(key.ID).TokensUsed != 300 {
		t.Fatal("new usage lost")
	}

	tmp, revision, usageRevision = makeSnapshot()
	if err := ResetApiKeyUsage(key.ID); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSettingsPatch(nil, nil, "new-password"); err != nil {
		t.Fatal(err)
	}
	if err := commitStatsSnapshot(tmp, path, revision, usageRevision); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if GetApiKeyEntry(key.ID).TokensUsed != 0 || cfg.Password != "new-password" {
		t.Fatal("old snapshot overwrote reset/settings")
	}
}

func TestUsageFlushFailureIsRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	key, err := AddApiKey(ApiKeyEntry{Key: "sk-retry", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordApiKeyUsage(key.ID, 100, 1); err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(t.TempDir(), "missing", "config.json")
	if err := UpdateStats(1, 1, 0, 100, 1); err == nil {
		t.Fatal("expected write failure")
	}
	if savedStatsRevision == statsRevision {
		t.Fatal("failed write marked saved")
	}
	cfgPath = path
	if err := UpdateStats(1, 1, 0, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if GetApiKeyEntry(key.ID).TokensUsed != 100 {
		t.Fatal("retry lost usage")
	}
}

func TestNormalizeAPIKeyAccountPipeRegionAndMachineId(t *testing.T) {
	account := Account{
		KiroApiKey: " ksk_test_key|eu-central-1 ",
		AuthMethod: "API KEY",
	}
	if err := NormalizeAPIKeyAccount(&account); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if account.KiroApiKey != "ksk_test_key" {
		t.Fatalf("key = %q", account.KiroApiKey)
	}
	if account.AccessToken != "ksk_test_key" {
		t.Fatalf("accessToken should mirror api key, got %q", account.AccessToken)
	}
	if account.AuthMethod != "api_key" {
		t.Fatalf("authMethod = %q", account.AuthMethod)
	}
	if account.Region != "eu-central-1" {
		t.Fatalf("region = %q", account.Region)
	}
	if account.RefreshToken != "" || account.ProfileArn != "" || account.ExpiresAt != 0 {
		t.Fatalf("oauth fields should be cleared: %+v", account)
	}
	wantMachine := MachineIdFromAPIKey("ksk_test_key")
	if account.MachineId != wantMachine {
		t.Fatalf("machineId = %q, want %q", account.MachineId, wantMachine)
	}
	if !IsAPIKeyAccount(&account) {
		t.Fatal("expected IsAPIKeyAccount true")
	}
}

func TestAddAccountRejectsDuplicateAPIKey(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	first := Account{ID: "api-1", KiroApiKey: "ksk_dup", AuthMethod: "api_key", Enabled: true}
	if err := AddAccount(first); err != nil {
		t.Fatalf("add first: %v", err)
	}
	second := Account{ID: "api-2", KiroApiKey: "ksk_dup", AuthMethod: "api_key", Enabled: true}
	if err := AddAccount(second); err != ErrDuplicateAPIKey {
		t.Fatalf("expected ErrDuplicateAPIKey, got %v", err)
	}
}

func TestSplitKiroAPIKeyAndRegionValidation(t *testing.T) {
	key, region, err := SplitKiroAPIKeyAndRegion("ksk_abc|us-east-1")
	if err != nil || key != "ksk_abc" || region != "us-east-1" {
		t.Fatalf("got key=%q region=%q err=%v", key, region, err)
	}
	if _, _, err := SplitKiroAPIKeyAndRegion("ksk_abc|us-east-1|extra"); err == nil {
		t.Fatal("expected multi-pipe error")
	}
	if _, _, err := SplitKiroAPIKeyAndRegion("|us-east-1"); err == nil {
		t.Fatal("expected empty key error")
	}
}

func TestUpdateSettingsPatchPreservesOmittedAPIKeyFields(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := UpdateSettingsPatch(nil, nil, "new-admin-password"); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "proxy-api-key" {
		t.Fatalf("expected API key to be preserved, got %q", got)
	}
	if !IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to stay enabled")
	}
	if got := GetPassword(); got != "new-admin-password" {
		t.Fatalf("expected password to update, got %q", got)
	}
}

func TestUpdateSettingsPatchCanExplicitlyDisableAPIKey(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	emptyKey := ""
	requireAPIKey := false
	if err := UpdateSettingsPatch(&emptyKey, &requireAPIKey, ""); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "" {
		t.Fatalf("expected API key to be cleared, got %q", got)
	}
	if IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to be disabled")
	}
	if got := GetPassword(); got != "admin-password" {
		t.Fatalf("expected password to be preserved, got %q", got)
	}
}

func TestUpdateAccountStaleSnapshotPreservesCredentialRotation(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{
		ID:            "rotation-account",
		AccessToken:   "access-1",
		RefreshToken:  "refresh-1",
		ClientID:      "client",
		AuthMethod:    "external_idp",
		Region:        "us-east-1",
		ExpiresAt:     100,
		ProfileArn:    "arn:aws:codewhisperer:us-east-1:123456789012:profile/one",
		TokenEndpoint: "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		IssuerURL:     "https://login.microsoftonline.com/tenant/v2.0",
		Scopes:        "scope-one",
		Enabled:       true,
	}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	stale := GetAccounts()[0]

	const rotatedProfile = "arn:aws:codewhisperer:eu-central-1:123456789012:profile/two"
	if err := UpdateAccountCredentialState(
		account.ID,
		"access-2",
		"refresh-2",
		200,
		rotatedProfile,
	); err != nil {
		t.Fatalf("rotate credential: %v", err)
	}

	stale.Enabled = false
	stale.BanStatus = "BANNED"
	stale.BanReason = "stale status update"
	if err := UpdateAccount(account.ID, stale); err != nil {
		t.Fatalf("apply stale status snapshot: %v", err)
	}

	got := GetAccounts()[0]
	if got.AccessToken != "access-2" ||
		got.RefreshToken != "refresh-2" ||
		got.ExpiresAt != 200 ||
		got.ProfileArn != rotatedProfile {
		t.Fatalf("stale status update reverted credential state: %+v", got)
	}
	if got.RefreshTokenFingerprint != RefreshTokenFingerprint("refresh-1") {
		t.Fatalf("original refresh token fingerprint = %q", got.RefreshTokenFingerprint)
	}
	if got.Enabled || got.BanStatus != "BANNED" || got.BanReason != "stale status update" {
		t.Fatalf("status fields were not applied: %+v", got)
	}
}

// TestAccountAllowOverageMigration verifies that a config.json from before the
// upstream-Overages-switch refactor (which carried `allowOverage: true` per
// account) is migrated into OverageStatus="ENABLED" on first load, and that
// the legacy field is cleared so future saves don't re-emit it.
func TestAccountAllowOverageMigration(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")

	seed := map[string]interface{}{
		"password":      "p",
		"port":          8080,
		"host":          "0.0.0.0",
		"requireApiKey": false,
		"accounts": []map[string]interface{}{
			{"id": "acc-allow", "enabled": true, "allowOverage": true},
			{"id": "acc-deny", "enabled": true, "allowOverage": false},
			{"id": "acc-already-set", "enabled": true, "allowOverage": true, "overageStatus": "DISABLED"},
		},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgFile, raw, 0600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := Init(cfgFile); err != nil {
		t.Fatalf("init: %v", err)
	}

	accounts := GetAccounts()
	byID := map[string]Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}

	if got := byID["acc-allow"].OverageStatus; got != "ENABLED" {
		t.Fatalf("expected acc-allow to migrate to OverageStatus=ENABLED, got %q", got)
	}
	if byID["acc-allow"].LegacyAllowOverage {
		t.Fatalf("expected legacy allowOverage to be cleared after migration")
	}
	if got := byID["acc-deny"].OverageStatus; got != "" {
		t.Fatalf("expected acc-deny to keep empty OverageStatus, got %q", got)
	}
	// Pre-set OverageStatus must win over the legacy field.
	if got := byID["acc-already-set"].OverageStatus; got != "DISABLED" {
		t.Fatalf("expected acc-already-set OverageStatus to be preserved, got %q", got)
	}
	if byID["acc-already-set"].LegacyAllowOverage {
		t.Fatalf("expected legacy field to still be cleared on acc-already-set")
	}

	// Re-read the file and confirm legacy field is gone (so it doesn't drift
	// back in on later saves).
	on_disk, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var reloaded struct {
		Accounts []map[string]interface{} `json:"accounts"`
	}
	if err := json.Unmarshal(on_disk, &reloaded); err != nil {
		t.Fatalf("decode reload: %v", err)
	}
	for _, a := range reloaded.Accounts {
		if _, ok := a["allowOverage"]; ok {
			t.Fatalf("expected allowOverage to be omitted from persisted file, got %+v", a)
		}
	}
}
