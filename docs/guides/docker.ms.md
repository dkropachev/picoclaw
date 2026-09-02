# 🐳 Panduan Docker & Quick Start

> Kembali ke [README](../project/README.ms.md)

## 🐳 Docker Compose

Anda juga boleh menjalankan PicoClaw menggunakan Docker Compose tanpa memasang apa-apa secara setempat.

```bash
# 1. Clone repo ini
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Mulakan pakej nod tunggal Web + API + Gateway
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Buka <http://localhost:18800/launcher-setup>, cipta kata laluan dashboard, kemudian konfigurasikan penyedia dan model dalam WebUI.

Arahan ini memulakan satu container yang merangkumi:

- WebUI terbina dalam dan API launcher pada port `18800` yang diterbitkan;
- proses anak Gateway yang diurus launcher pada loopback container;
- pangkalan data SQLite berasaskan fail dan data workspace yang disimpan bersama dalam `docker/data/`.

Secara lalai, port `18800` terikat hanya pada alamat loopback host (`127.0.0.1`). Lengkapkan `/launcher-setup` secara setempat sebelum sebarang pendedahan. Hanya jika akses LAN diminta, mulakan semula pakej dengan:

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Sembang dan media pelayar menggunakan proksi same-origin launcher yang disahkan, jadi port Gateway tidak perlu diterbitkan. Probe `GET`/`HEAD` awam pada `/health` dan `/ready` melaporkan ketersediaan launcher secara berasingan daripada konfigurasi Gateway atau model.

> [!WARNING]
> Konsol web dilindungi oleh kata laluan log masuk dashboard tetapi tidak menamatkan TLS. Untuk akses LAN, gunakan firewall atau reverse proxy TLS dan konfigurasikan kawalan CIDR launcher. Jangan dedahkannya terus kepada rangkaian tidak dipercayai.

### Akses terus Gateway (webhook)

Jika channel callback HTTP atau integrasi lanjutan memerlukan akses terus kepada Gateway, terbitkan port `18790` secara pilihan dengan fail tambahan:

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

Fail tambahan menukar alamat bind Gateway yang diurus kepada `0.0.0.0` dan menerbitkan port `18790`. Lindungi port ini dengan firewall atau reverse proxy TLS. Channel yang menggunakan socket, stream atau long-polling tidak memerlukan fail tambahan ini.

### Mod Gateway sahaja

Fail Compose asas hanya mengandungi launcher; service headless berada dalam `docker-compose.headless.yml`. Untuk menjalankan Gateway jangka panjang sahaja, sasarkan service tersebut secara jelas dan aktifkan profilenya:

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` menghentikan container launcher sedia ada dalam project Compose yang sama sebelum Gateway kendiri bermula, sekali gus menghalang dua pepohon proses Gateway daripada berkongsi fail PID dan direktori SQLite.

Pada volume baharu, core image menjana `docker/data/config.json` kemudian berhenti. Tetapkan nilai penyedia dan channel, kemudian mulakan semula service itu dengan `-d`. Mod Gateway sahaja menyediakan route webhook, Pico, kesihatan dan runtime terlindung pada port Gateway; ia tidak menyediakan WebUI launcher atau endpoint sembang REST umum.

### Mod Agent (One-shot)

```bash
# Tanyakan soalan
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "What is 2+2?"

# Mod interaktif
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### Log dan hentikan

```bash
# Semak log pakej lalai
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# Hentikan pakej
docker compose -f docker/docker-compose.yml down
```

### Kemas kini

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Semasa migrasi sekali sahaja daripada susun atur Compose berasaskan profile yang lama, hentikan dan buang container bernama tetap sebelum pelancaran pertama susun atur baharu. Tindakan ini tidak membuang direktori `docker/data/` yang dipasang sebagai bind mount:

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### Sandaran dan pemulihan

Hentikan semua proses PicoClaw sebelum menyalin atau memulihkan `docker/data/`. Anggap seluruh direktori sebagai satu snapshot supaya setiap pangkalan data SQLite kekal dipasangkan dengan fail `-wal`, `-shm` dan kunci yang sepadan. Jangan jalankan versi binary berlainan pada home/workspace yang sama.

### Runtime MCP penuh

Image launcher lalai ialah runtime Alpine minimum dan tidak merangkumi Node.js, Python atau `uv` untuk package MCP stdio. `docker-compose.full.yml` mengekalkan profile agent MCP penuh dan Gateway headless sedia ada; buat masa ini ia tidak menyediakan service launcher.

### 🚀 Quick Start

> [!TIP]
> Tetapkan API Key anda dalam `~/.picoclaw/config.json`. Dapatkan API Key: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). Carian web adalah pilihan — dapatkan [Tavily API](https://tavily.com) percuma (1000 pertanyaan percuma/bulan) atau [Brave Search API](https://brave.com/search/api) (2000 pertanyaan percuma/bulan).

**1. Inisialisasi**

```bash
picoclaw onboard
```

**2. Konfigurasi** (`~/.picoclaw/config.json`)

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

> **Baharu**: Format konfigurasi `model_list` membolehkan penambahan penyedia tanpa perubahan kod. Lihat [Konfigurasi Model](#konfigurasi-model-model_list) untuk butiran.
> `request_timeout` adalah pilihan dan menggunakan saat. Jika diabaikan atau ditetapkan kepada `<= 0`, PicoClaw menggunakan timeout lalai (120s).

**3. Dapatkan API Key**

* **Penyedia LLM**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Carian Web** (pilihan):
  * [Brave Search](https://brave.com/search/api) - Berbayar ($5/1000 pertanyaan, ~$5-6/bulan)
  * [Perplexity](https://www.perplexity.ai) - Carian berkuasa AI dengan antara muka sembang
  * [SearXNG](https://github.com/searxng/searxng) - Enjin meta-carian hos kendiri (percuma, tidak perlu API key)
  * [Tavily](https://tavily.com) - Dioptimumkan untuk AI Agents (1000 permintaan/bulan)
  * DuckDuckGo - Fallback terbina dalam (tidak memerlukan API key)

> **Nota**: Lihat `config.example.json` untuk templat konfigurasi penuh.

**4. Sembang**

```bash
picoclaw agent -m "What is 2+2?"
```

Itu sahaja! Anda kini mempunyai pembantu AI yang berfungsi dalam masa 2 minit.

---
