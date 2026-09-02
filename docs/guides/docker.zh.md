# 🐳 Docker 与快速开始

> 返回 [README](../project/README.zh.md)

## 🐳 Docker Compose

您也可以使用 Docker Compose 运行 PicoClaw，无需在本地安装任何环境。

```bash
# 1. 克隆仓库
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. 启动 Web + API + Gateway 单节点套件
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

打开 <http://localhost:18800/launcher-setup>，创建 dashboard 密码，然后在 WebUI 中配置提供商和模型。

此命令会启动一个容器，其中包含：

- 在已发布 `18800` 端口上运行的内嵌 WebUI 和 launcher API；
- 在容器回环地址上运行、由 launcher 管理的 Gateway 子进程；
- 一并持久保存在 `docker/data/` 中的文件型 SQLite 数据库和 workspace 数据。

默认情况下，`18800` 端口仅绑定到主机回环地址（`127.0.0.1`）。任何对外开放前，请先在本机完成 `/launcher-setup`。只有明确需要 LAN 访问时，才使用以下命令重启套件：

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

浏览器聊天和媒体通过 launcher 已认证的同源代理访问，因此无需发布 Gateway 端口。`/health` 和 `/ready` 上的公开 `GET`/`HEAD` 探针独立报告 launcher 可用性，不受 Gateway 或模型配置影响。

> [!WARNING]
> Web 控制台通过 dashboard 登录密码保护，但不终止 TLS。允许 LAN 访问时，请使用防火墙或 TLS 反向代理，并配置 launcher 的 CIDR 控制。不要将其直接暴露到不可信网络。

### 直接访问 Gateway（webhook）

如果 HTTP 回调 channel 或高级集成需要直接访问 Gateway，请使用附加文件选择性发布 `18790` 端口：

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

附加文件会将受管 Gateway 的绑定地址改为 `0.0.0.0` 并发布 `18790` 端口。请使用防火墙或 TLS 反向代理保护此端口。采用 socket、stream 或 long-polling 传输的 channel 不需要此附加文件。

### 仅 Gateway 模式

基础 Compose 文件仅包含 launcher；headless service 位于 `docker-compose.headless.yml`。如需只运行常驻 Gateway，请启用其 profile 并明确指定 service：

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` 会在独立 Gateway 启动前停止同一 Compose 项目中现有的 launcher 容器，避免两套 Gateway 进程树共享 PID 文件和 SQLite 目录。

使用全新 volume 时，core image 会生成 `docker/data/config.json` 后退出。设置提供商和 channel 值，然后加 `-d` 再次启动该 service。仅 Gateway 模式在 Gateway 端口提供 webhook、Pico、健康检查和受保护的 runtime route；它不提供 launcher WebUI 或通用 REST 聊天 endpoint。

### Agent 模式 (一次性运行)

```bash
# 提问
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "2+2 等于几？"

# 交互模式
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### 日志与停止

```bash
# 查看默认套件日志
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# 停止套件
docker compose -f docker/docker-compose.yml down
```

### 更新镜像

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

从旧版基于 profile 的 Compose 布局一次性迁移时，请在首次启动新布局前停止并删除旧布局中名称固定的容器。此操作不会删除 bind mount 的 `docker/data/` 目录：

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### 备份与恢复

复制或恢复 `docker/data/` 前，请停止所有 PicoClaw 进程。将整个目录视为同一个快照，确保每个 SQLite 数据库始终与其对应的 `-wal`、`-shm` 和锁文件配套。不要让不同版本的二进制文件使用同一个主目录/workspace。

### 完整 MCP Runtime

默认 launcher 镜像是最小化 Alpine runtime，不包含用于 stdio MCP package 的 Node.js、Python 或 `uv`。`docker-compose.full.yml` 保留了现有的完整 MCP agent 和 headless Gateway profile；它目前不提供 launcher service。

---

## 🚀 快速开始

> [!TIP]
> 在 `~/.picoclaw/config.json` 中设置您的 API Key。获取 API Key: [火山引擎 (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu (智谱)](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM)。网络搜索是 **可选的** — 获取免费的 [Tavily API](https://tavily.com) (每月 1000 次免费查询) 或 [Brave Search API](https://brave.com/search/api) (每月 2000 次免费查询)。

**1. 初始化 (Initialize)**

```bash
picoclaw onboard
```

**2. 配置 (Configure)** (`~/.picoclaw/config.json`)

```json
{
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "model_name": "gpt-5.4",
      "max_tokens": 8192,
      "temperature": 0.7,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "ark-code-latest",
      "provider": "volcengine",
      "model": "ark-code-latest",
      "api_keys": ["sk-your-api-key"],
      "api_base":"https://ark.cn-beijing.volces.com/api/coding/v3"
    },
    {
      "model_name": "gpt-5.4",
      "provider": "openai",
      "model": "gpt-5.4",
      "api_keys": ["your-api-key"],
      "request_timeout": 300
    },
    {
      "model_name": "claude-sonnet-4.6",
      "provider": "anthropic",
      "model": "claude-sonnet-4.6",
      "api_keys": ["your-anthropic-key"]
    }
  ],
  "tools": {
    "web": {
      "enabled": true,
      "fetch_limit_bytes": 10485760,
      "format": "plaintext",
      "brave": {
        "enabled": false,
        "api_key": "YOUR_BRAVE_API_KEY",
        "max_results": 5
      },
      "tavily": {
        "enabled": false,
        "api_key": "YOUR_TAVILY_API_KEY",
        "max_results": 5
      },
      "duckduckgo": {
        "enabled": true,
        "max_results": 5
      },
      "perplexity": {
        "enabled": false,
        "api_key": "YOUR_PERPLEXITY_API_KEY",
        "max_results": 5
      },
      "searxng": {
        "enabled": false,
        "base_url": "http://your-searxng-instance:8888",
        "max_results": 5
      }
    }
  }
}
```

> **新功能**: `model_list` 配置格式支持零代码添加 provider。详见[模型配置](providers.zh.md#模型配置-model_list)章节。
> `request_timeout` 为可选项，单位为秒。若省略或设置为 `<= 0`，PicoClaw 使用默认超时（120 秒）。

**3. 获取 API Key**

* **LLM 提供商**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **网络搜索** (可选):
  * [Brave Search](https://brave.com/search/api) - 付费 ($5/1000 次查询，约 $5-6/月)
  * [Perplexity](https://www.perplexity.ai) - AI 驱动的搜索与聊天界面
  * [SearXNG](https://github.com/searxng/searxng) - 自建元搜索引擎（免费，无需 API Key）
  * [Tavily](https://tavily.com) - 专为 AI Agent 优化 (1000 请求/月)
  * DuckDuckGo - 内置回退（无需 API Key）

> **注意**: 完整的配置模板请参考 `config.example.json`。

**4. 对话 (Chat)**

```bash
picoclaw agent -m "2+2 等于几？"
```

就是这样！您在 2 分钟内就拥有了一个可工作的 AI 助手。

---
