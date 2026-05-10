package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// Kiro API 工具名称最大长度限制
const kiroToolNameMaxLen = 63

// toolNameMap 存储 短名 -> 原名 的映射，用于 Kiro 响应中工具名回写。
//
// 同一原名永远映射到同一短名，所以并发请求间无冲突。映射在进程生命周期内保留，
// 长工具名总量在生产中通常 < 1000，无需 LRU。
var toolNameMap sync.Map

// shortenToolNameWithMap 缩短超长工具名，并记录映射用于回写。
//
// 算法：prefix(54) + "_" + sha256(name)[:8] = 63
func shortenToolNameWithMap(name string) string {
	if len(name) <= kiroToolNameMaxLen {
		return name
	}

	// MCP 工具优先尝试 mcp__server__tool -> mcp__tool（保持可读）
	if strings.HasPrefix(name, "mcp__") {
		if lastIdx := strings.LastIndex(name, "__"); lastIdx > 5 {
			candidate := "mcp__" + name[lastIdx+2:]
			if len(candidate) <= kiroToolNameMaxLen && candidate != name {
				toolNameMap.Store(candidate, name)
				return candidate
			}
		}
	}

	// 通用确定性缩短：截断前缀 + "_" + 8 位 sha256 hex
	sum := sha256.Sum256([]byte(name))
	hashSuffix := hex.EncodeToString(sum[:])[:8]
	prefixMax := kiroToolNameMaxLen - 1 - len(hashSuffix) // 54
	prefix := name
	if len([]rune(prefix)) > prefixMax {
		runes := []rune(name)
		prefix = string(runes[:prefixMax])
	}
	short := prefix + "_" + hashSuffix
	// 防止 ASCII/多字节边界字节数仍超限（极端情况下）
	if len(short) > kiroToolNameMaxLen {
		short = short[:kiroToolNameMaxLen-len(hashSuffix)-1] + "_" + hashSuffix
	}
	toolNameMap.Store(short, name)
	return short
}

// RestoreToolName 根据短名查找原始工具名；无映射则原样返回。
func RestoreToolName(name string) string {
	if v, ok := toolNameMap.Load(name); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return name
}
