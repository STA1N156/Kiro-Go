// Package main provides the entry point for Kiro API Proxy.
//
// Kiro API Proxy is a reverse proxy service that translates Kiro API requests
// into OpenAI and Anthropic (Claude) compatible formats. Key features include:
//   - Multi-account pool with round-robin load balancing
//   - Automatic OAuth token refresh
//   - Streaming response support for real-time AI interactions
//   - Admin panel for account and configuration management
//
// The service exposes the following endpoints:
//   - /v1/messages - Claude API compatible endpoint
//   - /v1/chat/completions - OpenAI API compatible endpoint
//   - /admin - Web-based administration panel
package main

import (
	"fmt"
	"kiro-go/config"
	"kiro-go/pool"
	"kiro-go/proxy"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	// 配置文件路径，支持环境变量覆盖
	configPath := "data/config.json"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}

	// 确保数据目录存在
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// 加载配置
	if err := config.Init(configPath); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 环境变量覆盖密码
	if envPassword := os.Getenv("ADMIN_PASSWORD"); envPassword != "" {
		config.SetPassword(envPassword)
	}

	// 环境变量 KIRO_API_KEY：注入 Kiro CLI headless 模式凭据
	if envApiKey := os.Getenv("KIRO_API_KEY"); envApiKey != "" {
		ensureApiKeyAccount(envApiKey)
	}

	// 初始化账号池
	pool.GetPool()

	// 创建 HTTP 处理器（包含后台刷新任务）
	handler := proxy.NewHandler()

	// 启动服务器
	addr := fmt.Sprintf("%s:%d", config.GetHost(), config.GetPort())
	log.Printf("Kiro-Go starting on http://%s", addr)
	log.Printf("Admin panel: http://%s/admin", addr)
	log.Printf("Claude API: http://%s/v1/messages", addr)
	log.Printf("OpenAI API: http://%s/v1/chat/completions", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// ensureApiKeyAccount 确保配置里存在以指定 API Key 作为 access token 的凭据。
//
// 同一 API Key 多次启动只保留一份；启动失败仅打印日志，不阻断主流程。
func ensureApiKeyAccount(apiKey string) {
	for _, a := range config.GetAccounts() {
		if a.AuthMethod == "api_key" && a.AccessToken == apiKey {
			return
		}
	}
	account := config.Account{
		ID:          uuidLikeID(),
		Nickname:    "KIRO_API_KEY",
		AccessToken: apiKey,
		AuthMethod:  "api_key",
		Provider:    "ApiKey",
		Region:      "us-east-1",
		Enabled:     true,
		MachineId:   config.GenerateMachineId(),
	}
	if err := config.AddAccount(account); err != nil {
		log.Printf("[KIRO_API_KEY] failed to register: %v", err)
		return
	}
	log.Printf("[KIRO_API_KEY] registered headless credential id=%s", account.ID)
}

// uuidLikeID 用 config 包内的随机生成器构造一个 UUID 形式的字符串。
//
// 不直接 import google/uuid 是为了让 main.go 仅依赖标准库；config.GenerateMachineId
// 已用 crypto/rand 给出 UUID v4 形式，复用即可。
func uuidLikeID() string {
	return config.GenerateMachineId()
}
