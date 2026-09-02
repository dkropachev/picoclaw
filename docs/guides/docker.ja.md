# 🐳 Docker とクイックスタート

> [README](../project/README.ja.md) に戻る

## 🐳 Docker Compose

Docker Compose を使用して PicoClaw を実行できます。ローカルに何もインストールする必要はありません。

```bash
# 1. リポジトリをクローン
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Web + API + Gateway のシングルノード構成を起動
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

<http://localhost:18800/launcher-setup> を開き、dashboard パスワードを作成してから、WebUI で provider と model を設定します。

このコマンドは、次の要素を含む 1 つのコンテナを起動します。

- 公開ポート `18800` 上の組み込み WebUI と Launcher API
- コンテナの loopback 上で Launcher が管理する Gateway 子プロセス
- `docker/data/` にまとめて永続化されるファイルベースの SQLite データベースと workspace データ

デフォルトでは、ポート `18800` はホストの loopback address（`127.0.0.1`）だけに bind されます。外部に公開する前に、ローカルで `/launcher-setup` を完了してください。LAN access が明示的に必要な場合に限り、次のコマンドで構成を再起動します。

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

ブラウザのチャットとメディアは Launcher の認証済み same-origin proxy を経由するため、Gateway ポートを公開する必要はありません。`/health` と `/ready` の公開 `GET`/`HEAD` probe は、Gateway や model の設定とは独立して Launcher の稼働状況を返します。

> [!WARNING]
> Web コンソールは dashboard ログインパスワードで保護されますが、TLS の終端は行いません。LAN access には firewall または TLS reverse proxy を使用し、Launcher の CIDR 制御を設定してください。信頼できないネットワークに直接公開しないでください。

### Gateway への直接アクセス（Webhook）

HTTP callback channel や高度な integration が Gateway への直接アクセスを必要とする場合は、追加ファイルを使ってポート `18790` を明示的に公開します。

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

追加ファイルは、管理対象 Gateway の bind address を `0.0.0.0` に変更して `18790` を公開します。このポートを firewall または TLS reverse proxy で保護してください。socket、stream、long-polling を使用する channel には、この追加ファイルは不要です。

### Gateway のみのモード

基本の Compose file には Launcher だけが含まれ、headless service は `docker-compose.headless.yml` にあります。常駐 Gateway だけを起動するには、profile を有効にして service を明示的に指定します。

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` は、standalone Gateway の起動前に同じ Compose project の既存 Launcher container を停止し、2 つの Gateway process tree が PID file と SQLite home を共有するのを防ぎます。

新しい volume では、core image が `docker/data/config.json` を生成して終了します。provider と channel の値を設定し、`-d` を付けて同じ service を再度起動してください。Gateway のみのモードは、Gateway ポート上で webhook、Pico、health、および保護された runtime route を提供しますが、Launcher WebUI や汎用 REST chat endpoint は提供しません。

### Agent モード (ワンショット)

```bash
# 質問する
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "2+2は？"

# インタラクティブモード
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### ログと停止

```bash
# デフォルト構成のログを確認
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# 構成を停止
docker compose -f docker/docker-compose.yml down
```

### イメージの更新

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

以前の profile ベースの Compose 構成から一度だけ移行する際は、新しい構成を初めて起動する前に、固定名の既存 container を停止して削除してください。bind mount された `docker/data/` ディレクトリは削除されません。

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### バックアップと復元

`docker/data/` をコピーまたは復元する前に、すべての PicoClaw プロセスを停止してください。ディレクトリ全体を 1 つの snapshot として扱い、各 SQLite データベースと対応する `-wal`、`-shm`、lock file の組み合わせを維持してください。同じ home/workspace に対して異なるバージョンの binary を同時に実行しないでください。

### フル MCP Runtime

デフォルトの Launcher image は最小構成の Alpine runtime であり、stdio MCP package 用の Node.js、Python、`uv` は含まれていません。`docker-compose.full.yml` には既存のフル MCP agent と headless Gateway の profile がありますが、現在 Launcher service はありません。

---

## 🚀 クイックスタート

> [!TIP]
> `~/.picoclaw/config.json` に API Key を設定してください。API Key の取得先: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM)。Web 検索は**オプション**です — 無料の [Tavily API](https://tavily.com) (月 1000 回無料) または [Brave Search API](https://brave.com/search/api) (月 2000 回無料) を取得できます。

**1. 初期化**

```bash
picoclaw onboard
```

**2. 設定** (`~/.picoclaw/config.json`)

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
      "model": "volcengine/ark-code-latest",
      "api_keys": ["sk-your-api-key"],
      "api_base":"https://ark.cn-beijing.volces.com/api/coding/v3"
    },
    {
      "model_name": "gpt-5.4",
      "model": "openai/gpt-5.4",
      "api_keys": ["your-api-key"],
      "request_timeout": 300
    },
    {
      "model_name": "claude-sonnet-4.6",
      "model": "anthropic/claude-sonnet-4.6",
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

> **新機能**: `model_list` 設定形式により、コード変更なしで provider を追加できます。詳細は[モデル設定](providers.ja.md#モデル設定-model_list)を参照してください。
> `request_timeout` はオプションで、単位は秒です。省略または `<= 0` に設定した場合、PicoClaw はデフォルトのタイムアウト（120 秒）を使用します。

**3. API Key の取得**

* **LLM プロバイダー**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Web 検索** (オプション):
  * [Brave Search](https://brave.com/search/api) - 有料 ($5/1000 queries, ~$5-6/month)
  * [Perplexity](https://www.perplexity.ai) - AI 搭載の検索・チャットインターフェース
  * [SearXNG](https://github.com/searxng/searxng) - セルフホスト型メタ検索エンジン（無料、API Key 不要）
  * [Tavily](https://tavily.com) - AI Agent 向けに最適化 (1000 requests/month)
  * DuckDuckGo - 組み込みフォールバック（API Key 不要）

> **注意**: 完全な設定テンプレートは `config.example.json` を参照してください。

**4. チャット**

```bash
picoclaw agent -m "2+2は？"
```

以上です！2 分で動作する AI アシスタントが手に入ります。

---
