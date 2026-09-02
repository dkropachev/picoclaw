# 🐳 Docker và Bắt Đầu Nhanh

> Quay lại [README](../project/README.vi.md)

## 🐳 Docker Compose

Bạn cũng có thể chạy PicoClaw bằng Docker Compose mà không cần cài đặt gì trên máy.

```bash
# 1. Clone repo này
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Khởi động gói một nút Web + API + Gateway
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Mở <http://localhost:18800/launcher-setup>, tạo mật khẩu dashboard, sau đó cấu hình nhà cung cấp và mô hình trong WebUI.

Lệnh này khởi động một container chứa:

- WebUI tích hợp và API launcher trên port `18800` được công khai;
- tiến trình con Gateway do launcher quản lý trên loopback của container;
- cơ sở dữ liệu SQLite dạng file và dữ liệu workspace được lưu lâu dài cùng nhau trong `docker/data/`.

Theo mặc định, port `18800` chỉ bind vào địa chỉ loopback của host (`127.0.0.1`). Hoàn tất `/launcher-setup` trên máy cục bộ trước khi công khai. Chỉ khi có yêu cầu truy cập LAN, hãy khởi động lại gói bằng:

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Chat và media trong trình duyệt sử dụng các proxy same-origin đã xác thực của launcher, vì vậy không cần công khai port Gateway. Các probe `GET`/`HEAD` công khai tại `/health` và `/ready` báo cáo tình trạng hoạt động của launcher độc lập với cấu hình Gateway hoặc mô hình.

> [!WARNING]
> Web console được bảo vệ bằng mật khẩu đăng nhập dashboard nhưng không kết thúc TLS. Khi truy cập qua LAN, hãy sử dụng firewall hoặc reverse proxy TLS và cấu hình kiểm soát CIDR của launcher. Không để lộ trực tiếp ra mạng không tin cậy.

### Truy cập trực tiếp Gateway (webhook)

Nếu channel callback HTTP hoặc integration nâng cao cần truy cập trực tiếp Gateway, hãy chủ động công khai port `18790` bằng file bổ sung:

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

File bổ sung đổi địa chỉ bind của Gateway được quản lý thành `0.0.0.0` và công khai port `18790`. Bảo vệ port này bằng firewall hoặc reverse proxy TLS. Các channel sử dụng socket, stream hoặc long-polling không cần file bổ sung này.

### Chế độ chỉ Gateway

File Compose cơ sở chỉ chứa launcher; các service headless nằm trong `docker-compose.headless.yml`. Để chỉ chạy Gateway dài hạn, hãy chỉ định rõ service và bật profile của nó:

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` dừng mọi container launcher hiện có trong cùng project Compose trước khi Gateway độc lập khởi động, ngăn hai cây tiến trình Gateway dùng chung file PID và thư mục SQLite.

Trên volume mới, core image tạo `docker/data/config.json` rồi thoát. Đặt các giá trị nhà cung cấp và channel, sau đó khởi động lại service đó với `-d`. Chế độ chỉ Gateway phục vụ các route webhook, Pico, tình trạng và runtime được bảo vệ trên port Gateway; chế độ này không cung cấp WebUI launcher hoặc các endpoint chat REST chung.

### Chế Độ Agent (One-shot)

```bash
# Đặt câu hỏi
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "What is 2+2?"

# Chế độ tương tác
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### Log và dừng

```bash
# Kiểm tra log của gói mặc định
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# Dừng gói
docker compose -f docker/docker-compose.yml down
```

### Cập Nhật

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Khi di chuyển một lần từ bố cục Compose cũ dựa trên profile, hãy dừng và xóa các container có tên cố định trước lần khởi động đầu tiên với bố cục mới. Thao tác này không xóa thư mục `docker/data/` được gắn bằng bind mount:

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### Sao lưu và khôi phục

Dừng mọi tiến trình PicoClaw trước khi sao chép hoặc khôi phục `docker/data/`. Coi toàn bộ thư mục là một snapshot để mỗi cơ sở dữ liệu SQLite luôn đi cùng các file `-wal`, `-shm` và file khóa tương ứng. Không chạy các phiên bản binary khác nhau trên cùng một home/workspace.

### Runtime MCP đầy đủ

Image launcher mặc định là runtime Alpine tối thiểu và không bao gồm Node.js, Python hoặc `uv` cho các package MCP stdio. `docker-compose.full.yml` giữ lại các profile agent MCP đầy đủ và Gateway headless hiện có; hiện tại file này không cung cấp service launcher.

### 🚀 Bắt Đầu Nhanh

> [!TIP]
> Cấu hình API Key trong `~/.picoclaw/config.json`. Lấy API Key: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). Tìm kiếm web là tùy chọn — lấy miễn phí [Tavily API](https://tavily.com) (1000 truy vấn miễn phí/tháng) hoặc [Brave Search API](https://brave.com/search/api) (2000 truy vấn miễn phí/tháng).

**1. Khởi tạo**

```bash
picoclaw onboard
```

**2. Cấu hình** (`~/.picoclaw/config.json`)

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

> **Mới**: Định dạng cấu hình `model_list` cho phép thêm provider mà không cần thay đổi code. Xem [Cấu Hình Mô Hình](#cấu-hình-mô-hình-model_list) để biết chi tiết.
> `request_timeout` là tùy chọn và tính bằng giây. Nếu bỏ qua hoặc đặt `<= 0`, PicoClaw sử dụng timeout mặc định (120s).

**3. Lấy API Key**

* **Nhà cung cấp LLM**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Tìm kiếm Web** (tùy chọn):
  * [Brave Search](https://brave.com/search/api) - Trả phí ($5/1000 truy vấn, ~$5-6/tháng)
  * [Perplexity](https://www.perplexity.ai) - Tìm kiếm bằng AI với giao diện chat
  * [SearXNG](https://github.com/searxng/searxng) - Công cụ tìm kiếm tổng hợp tự host (miễn phí, không cần API key)
  * [Tavily](https://tavily.com) - Tối ưu cho AI Agent (1000 yêu cầu/tháng)
  * DuckDuckGo - Fallback tích hợp (không cần API key)

> **Lưu ý**: Xem `config.example.json` để có mẫu cấu hình đầy đủ.

**4. Chat**

```bash
picoclaw agent -m "What is 2+2?"
```

Vậy là xong! Bạn có một trợ lý AI hoạt động trong 2 phút.

---
