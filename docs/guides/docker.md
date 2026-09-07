# 🐳 Docker & Quick Start Guide

> Back to [README](../README.md)

## 🐳 Docker Compose

You can also run PicoClaw using Docker Compose without installing anything locally.

```bash
# 1. Clone this repo
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Start the default single-node bundle
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Open <http://localhost:18800/launcher-setup>, create the dashboard password,
then configure a provider and model in the WebUI.

This starts one container containing:

- the embedded WebUI and launcher API on port `18800`, mapped only to host
  loopback by default;
- the Gateway runtime hosted by that launcher process, with its internal
  listener on container loopback;
- file-backed SQLite databases and workspace data persisted together under
  `docker/data/`.

Compose grants the launcher 120 seconds to cancel and join the embedded runtime
before container termination, including active workflow and SQLite cleanup.

Browser chat and media use authenticated same-origin launcher proxies, so the
Gateway port does not need to be published. Public `GET`/`HEAD` probes at
`/health` and `/ready` report launcher availability independently of Gateway or
model configuration.

Complete `/launcher-setup` locally before allowing LAN access. When remote
launcher access is explicitly required, change the host-side bind with the
Compose interpolation variable and protect it with a firewall, TLS reverse
proxy, and launcher CIDR controls:

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 \
  docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

> [!WARNING]
> Dashboard password protection begins after initial setup, and the launcher does not terminate TLS. Never expose an uninitialized launcher or publish it directly to an untrusted network.

### Direct Gateway Access

HTTP callback channels and advanced integrations may need direct access to the
Gateway listener. Opt in to public port `18790` with the override file:

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

The override changes the in-process Gateway listener bind address to `0.0.0.0`
and publishes `18790`. It does not start another process. Protect that port with
a firewall or TLS reverse proxy. Channels using socket, stream, or long-polling
delivery do not need this override.

### Gateway-Only Mode

The optional `gateway` profile keeps a standalone headless deployment available
as an explicit alternative to the launcher. Name the service explicitly from
the separate headless file:

```bash
docker compose -f docker/docker-compose.headless.yml \
  --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` stops an existing launcher container in the same Compose
project before the standalone Gateway starts. The default launcher startup uses
the same option for the reverse transition, preventing the launcher-hosted
runtime and a standalone Gateway process from sharing the PID file and SQLite
home.

On a fresh volume, the core image generates `docker/data/config.json` and exits.
Set provider/channel values, then start that service again with `-d`. Gateway-only
mode serves webhook, Pico, health, and protected runtime routes on the Gateway
port; it does not provide the launcher WebUI or generic REST chat endpoints.

### Logs And Stop

```bash
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher
docker compose -f docker/docker-compose.yml down
```

### Agent Mode (One-shot)

```bash
# Ask a question
docker compose -f docker/docker-compose.headless.yml \
  run --rm picoclaw-agent -m "What is 2+2?"

# Interactive mode
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### Update

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

When upgrading once from the older profile-based Compose layout, stop and
remove its fixed-name containers before the first new start. This does not
remove the bind-mounted `docker/data/` directory:

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### Backup And Restore

Stop every PicoClaw process before copying or restoring `docker/data/`. Treat the
whole directory as one snapshot so every SQLite database stays paired with its
matching `-wal`, `-shm`, and lock files. Do not run mixed binary versions against
one home/workspace.

### Full MCP Runtime

The default launcher image is the minimal Alpine runtime and does not bundle
Node.js, Python, or `uv` for stdio MCP packages. `docker-compose.full.yml` keeps
the existing full MCP agent and headless Gateway profiles; it does not currently
provide a launcher service.

### 🚀 Quick Start

> [!TIP]
> Set your API Key in `~/.picoclaw/config.json`. Get API Keys: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). Web search is optional — get a free [Tavily API](https://tavily.com) (1000 free queries/month) or [Brave Search API](https://brave.com/search/api) (2000 free queries/month).

**1. Initialize**

```bash
picoclaw onboard
```

**2. Configure** (`~/.picoclaw/config.json`)

```json
{
  "version": 4,
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "account_ref": "openai-work",
      "model_name": "coding",
      "max_tokens": 8192,
      "temperature": 0.7,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "volcengine-work",
      "provider": "volcengine",
      "model": "",
      "api_keys": ["sk-your-api-key"],
      "api_base":"https://ark.cn-beijing.volces.com/api/coding/v3"
    },
    {
      "model_name": "openai-work",
      "provider": "openai",
      "model": "",
      "api_keys": ["your-api-key"],
      "request_timeout": 300
    },
    {
      "model_name": "anthropic-work",
      "provider": "anthropic",
      "model": "",
      "api_keys": ["your-anthropic-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "coding",
      "model": "gpt-5.4",
      "account_overrides": {
        "volcengine-work": "ark-code-latest",
        "anthropic-work": "claude-sonnet-4.6"
      }
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

> `model_list[]` configures provider accounts, while `model_aliases[]` configures
> executable model policy. See
> [Accounts and Model Aliases](configuration.md#accounts-and-model-aliases).
> `request_timeout` is optional and uses seconds. If omitted or set to `<= 0`, PicoClaw uses the default timeout (120s).

**3. Get API Keys**

* **LLM Provider**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Web Search** (optional):
  * [Brave Search](https://brave.com/search/api) - Paid ($5/1000 queries, ~$5-6/month)
  * [Perplexity](https://www.perplexity.ai) - AI-powered search with chat interface
  * [SearXNG](https://github.com/searxng/searxng) - Self-hosted metasearch engine (free, no API key needed)
  * [Tavily](https://tavily.com) - Optimized for AI Agents (1000 requests/month)
  * DuckDuckGo - Built-in fallback (no API key required)

> **Note**: See `config.example.json` for a complete configuration template.

**4. Chat**

```bash
picoclaw agent -m "What is 2+2?"
```

That's it! You have a working AI assistant in 2 minutes.

---
