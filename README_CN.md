# Kiro-Go

## 本地前缀缓存

管理后台 → 设置 → 本地前缀缓存，可开启/关闭并设置有效期。默认开启、5 分钟，可设置 1–10080 分钟，保存后对新请求立即生效。

- 基础连续前缀匹配，跨上游账号、下游密钥及模型共享；不使用 RP 分块，也不复用生成的回答。工具调用 ID 会规范化，参数和工具返回内容仍参与匹配。
- 只有成功完成的请求才写入指纹。相同请求最多回报输入的 **99.8%**（向下取整）；追加对话按最长相同前缀计算。同模型优先使用该前缀之前观测到的输入量，跨模型按前缀字节占比估算。
- OpenAI 返回 `usage.prompt_tokens_details.cached_tokens`；Responses 返回 `usage.input_tokens_details.cached_tokens`；Anthropic 返回 `cache_read_input_tokens`，其 `input_tokens` 为扣除缓存后的输入，两者相加才是总输入。缓存写入 token 为 0，不额外收取模拟写入量。
- 不修改输出 token。Kiro 原有的输入/输出 token 估算方式保持不变；本地命中是转发层统计，**不会降低 Kiro 官方额度**。关闭后不注入本地缓存量（无上游缓存数据时为 0 或不提供缓存字段）。
- 指纹计算与上游请求并行，流式正文不等待缓存；最终 usage 统一结算。索引分片加锁，后台每 30 秒清理并保存到配置同目录的 `prompt-cache.bin`，只含哈希与计数，不含聊天原文或密钥。意外退出最多丢失最近 30 秒新增索引。
- 索引最多 131072 条，二进制文件约 10 MiB 上限（低于 512 MiB），达到分片容量后会提前淘汰部分记录。部署时保留 `/app/data` 挂载以跨重启保留有效缓存。单个数据目录只运行一个实例。

用量统计实时更新内存，后台每 3 秒将全局、账号和下游密钥统计合并写盘，无变动时不写入。序列化与临时文件写入不占用配置锁，旧快照不会覆盖后台的新设置；正常停止时补存一次。意外断电或强制结束可能丢失最近约 3 秒统计（写盘持续失败时可能更久），但运行中的密钥限额检查仍然实时生效。非流式正文、思考和搜索回答使用缓冲区追加，避免长回复反复复制已有内容。

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

将 Kiro 账号转换为 OpenAI / Anthropic 兼容的 API 服务。

[English](README.md) | 中文 | [Tiếng Việt](README_VI.md)

如果这个项目帮到了你，欢迎点个 Star 支持一下。

## 功能特性

- Anthropic `/v1/messages`、OpenAI `/v1/chat/completions` 与 OpenAI `/v1/responses`
- 多账号池轮询负载均衡
- 自动 Token 刷新、SSE 流式输出、Web 管理面板
- 多种认证方式：AWS Builder ID、IAM Identity Center (企业 SSO)、Microsoft 企业 SSO、SSO Token、本地缓存、凭证 JSON、Kiro API Key
- 用量追踪、账号导入导出、中英越三语
- 支持设置出站代理（SOCKS5 / HTTP）

## 快速开始

### Docker Compose（推荐）

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker 运行

```bash
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  ghcr.io/quorinex/kiro-go:latest
```

### 源码编译

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### 部署到 Zeabur

仓库已包含 `Dockerfile`，可直接在 Zeabur 上构建运行。

**方式一：面板一键部署**

1. Fork 本仓库到你的 GitHub 账号。
2. 在 Zeabur 新建服务，选择 **Deploy from GitHub**，绑定刚才 fork 的仓库。
3. Zeabur 自动识别 `Dockerfile` 并完成构建。
4. 在 **Networking** 标签暴露端口 `8080` 并绑定域名。
5. 在 **Variables** 标签至少设置 `ADMIN_PASSWORD`（管理面板密码）。
6. 如需持久化账号 / 配置，挂载 Volume 到 `/app/data`。

**方式二：CLI 部署**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> 命令需在项目根目录执行。CLI 会生成 `.zeabur/context.json` 记录目标 project / service，包含个人 ID，请勿提交。

部署完成后访问 `https://<你的域名>/admin` 登录管理面板。

首次运行会在 `data/config.json` 自动生成配置，挂载 `/app/data` 以持久化。默认管理密码为 `changeme`，生产环境请务必通过 `ADMIN_PASSWORD` 环境变量或在管理面板中修改。

## 使用方法

访问 `http://localhost:8080/admin` 登录、添加账号，然后调用 API：

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"你好！"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好！"}]}'
```

### 添加 Kiro API Key 账号

管理面板「添加账号」可选择 **API Key**，填写 `ksk_...`（也支持 `ksk_...|region`）。

也可通过凭证导入接口添加：

```bash
curl -X POST http://localhost:8080/admin/api/auth/credentials \
  -H "Content-Type: application/json" \
  -H "Cookie: <admin-session>" \
  -d '{"kiroApiKey":"ksk_your_key|us-east-1","authMethod":"api_key","nickname":"cli-key"}'
```

API Key 账号会走 Kiro CLI runtime（`https://runtime.{region}.kiro.dev/`），请求头带 `tokentype: API_KEY`，无需 OAuth 刷新，也不使用 `profileArn`。

## 思考模式

在模型名后加后缀（默认 `-thinking`）即可启用，例如 `claude-sonnet-4.5-thinking`。Claude 兼容请求如果带有顶层 `thinking` 配置，例如 `{"type":"enabled","budget_tokens":2048}` 或 `{"type":"adaptive"}`，也会自动启用 thinking 模式。输出格式可在管理面板「设置 - Thinking 模式」中配置。

## 出站代理

可在管理面板「设置 - 出站代理设置」中配置代理。支持 SOCKS5 和 HTTP 代理。

设置保存后即时生效，无需重启服务。

## 环境变量

| 变量 | 说明 | 默认值 |
|-----|------|-------|
| `CONFIG_PATH` | 配置文件路径 | `data/config.json` |
| `ADMIN_PASSWORD` | 管理面板密码（覆盖配置文件） | - |

## 参与贡献

欢迎友好交流。遇到问题时，建议先让 Claude Code、Codex 等工具帮忙排查一下，大部分问题都能自己解决。如果能直接提个 PR 就更好了。

## 友情链接

- [LINUX DO](https://linux.do)

## 免责声明

本项目仅供学习和研究目的使用，与 Amazon、AWS 或 Kiro 没有任何关联。用户需自行确保使用行为符合所有适用的服务条款和法律法规，使用风险自负。

## 许可证

[MIT](LICENSE)
