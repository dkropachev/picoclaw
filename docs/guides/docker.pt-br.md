# 🐳 Docker e Início Rápido

> Voltar ao [README](../project/README.pt-br.md)

## 🐳 Docker Compose

Você também pode executar o PicoClaw usando Docker Compose sem instalar nada localmente.

```bash
# 1. Clone este repositório
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Inicie o pacote de nó único Web + API + Gateway
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Abra <http://localhost:18800/launcher-setup>, crie a senha do dashboard e configure um provedor e um modelo na WebUI.

Isso inicia um único contêiner com:

- a WebUI integrada e a API do launcher na porta publicada `18800`;
- o processo filho Gateway gerenciado pelo launcher no loopback do contêiner;
- bancos de dados SQLite baseados em arquivos e dados do workspace, persistidos juntos em `docker/data/`.

Por padrão, a porta `18800` é vinculada somente ao endereço de loopback do host (`127.0.0.1`). Conclua `/launcher-setup` localmente antes de qualquer exposição. Somente quando o acesso pela LAN for solicitado, reinicie o pacote com:

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

O chat e a mídia do navegador usam proxies same-origin autenticados do launcher, portanto a porta do Gateway não precisa ser publicada. As sondagens públicas `GET`/`HEAD` em `/health` e `/ready` informam a disponibilidade do launcher independentemente da configuração do Gateway ou do modelo.

> [!WARNING]
> O console web é protegido por senha de login do dashboard, mas não encerra TLS. Para acesso pela LAN, use firewall ou proxy reverso TLS e configure os controles CIDR do launcher. Não o exponha diretamente a uma rede não confiável.

### Acesso direto ao Gateway (webhooks)

Se um canal de callback HTTP ou uma integração avançada precisar de acesso direto ao Gateway, publique opcionalmente a porta `18790` com o arquivo complementar:

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

O arquivo complementar altera o endereço de bind do Gateway gerenciado para `0.0.0.0` e publica a porta `18790`. Proteja essa porta com firewall ou proxy reverso TLS. Canais que usam socket, stream ou long-polling não precisam desse arquivo.

### Modo somente Gateway

O arquivo Compose base contém somente o launcher; os serviços headless ficam em `docker-compose.headless.yml`. Para executar apenas o Gateway de longa duração, indique explicitamente o serviço e ative o perfil dele:

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` encerra qualquer contêiner launcher existente no mesmo projeto Compose antes de iniciar o Gateway independente, impedindo que duas árvores de processos Gateway compartilhem o arquivo PID e o diretório SQLite.

Em um volume novo, a imagem principal gera `docker/data/config.json` e encerra. Defina os valores do provedor e dos canais e inicie o serviço novamente com `-d`. O modo somente Gateway oferece rotas de webhook, Pico, saúde e runtime protegido na porta do Gateway; ele não fornece a WebUI do launcher nem endpoints REST genéricos de chat.

### Modo Agent (One-shot)

```bash
# Fazer uma pergunta
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "What is 2+2?"

# Modo interativo
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### Logs e encerramento

```bash
# Verifique os logs do pacote padrão
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# Encerre o pacote
docker compose -f docker/docker-compose.yml down
```

### Atualização

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Ao migrar uma única vez do layout Compose antigo baseado em perfis, encerre e remova os contêineres de nome fixo antes da primeira inicialização com o layout novo. Isso não remove o diretório `docker/data/` montado via bind mount:

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### Backup e restauração

Encerre todos os processos do PicoClaw antes de copiar ou restaurar `docker/data/`. Trate o diretório inteiro como um único snapshot para que cada banco de dados SQLite permaneça associado aos arquivos `-wal`, `-shm` e de bloqueio correspondentes. Não execute versões diferentes dos binários no mesmo diretório principal/workspace.

### Runtime MCP completo

A imagem padrão do launcher é o runtime Alpine mínimo e não inclui Node.js, Python nem `uv` para pacotes MCP stdio. O `docker-compose.full.yml` mantém os perfis existentes de agente MCP completo e Gateway headless; no momento, ele não fornece um serviço launcher.

### 🚀 Início Rápido

> [!TIP]
> Configure sua chave de API em `~/.picoclaw/config.json`. Obtenha chaves de API: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). A busca na web é opcional — obtenha gratuitamente uma [API Tavily](https://tavily.com) (1000 consultas gratuitas/mês) ou [API Brave Search](https://brave.com/search/api) (2000 consultas gratuitas/mês).

**1. Inicializar**

```bash
picoclaw onboard
```

**2. Configurar** (`~/.picoclaw/config.json`)

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

> **Novo**: O formato de configuração `model_list` permite adicionar provedores sem alteração de código. Veja [Configuração de Modelos](#configuração-de-modelos-model_list) para detalhes.
> `request_timeout` é opcional e usa segundos. Se omitido ou definido como `<= 0`, o PicoClaw usa o timeout padrão (120s).

**3. Obter chaves de API**

* **Provedor LLM**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Busca na Web** (opcional):
  * [Brave Search](https://brave.com/search/api) - Pago ($5/1000 consultas, ~$5-6/mês)
  * [Perplexity](https://www.perplexity.ai) - Busca com IA e interface de chat
  * [SearXNG](https://github.com/searxng/searxng) - Metabuscador auto-hospedado (gratuito, sem necessidade de chave de API)
  * [Tavily](https://tavily.com) - Otimizado para agentes de IA (1000 requisições/mês)
  * DuckDuckGo - Fallback integrado (sem necessidade de chave de API)

> **Nota**: Veja `config.example.json` para um modelo de configuração completo.

**4. Conversar**

```bash
picoclaw agent -m "What is 2+2?"
```

Pronto! Você tem um assistente de IA funcionando em 2 minutos.

---
