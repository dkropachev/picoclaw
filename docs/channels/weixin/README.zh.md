# 💬 微信个人号渠道 (Weixin)

PicoClaw 支持使用腾讯官方 iLink API 连接您的个人微信账号。

## 🚀 快速激活

最简单的方法是使用交互式 onboarding 命令进行一键激活：

```bash
picoclaw auth weixin
```

该命令将：
1. 从 iLink API 获取二维码并在终端中打印。
2. 等待您使用手机微信 App 扫码。
3. 扫码确认后，自动将生成的 Access Token 保存至您的 `~/.picoclaw/config.json` 中。

配置完成后，即可启动网关：

```bash
picoclaw gateway
```

---

## ⚙️ 配置说明

您也可以在 `config.json` 的 `channels.weixin` 段目下进行手动维护。

```json
{
  "channel_list": {
    "weixin": {
      "enabled": true,
      "type": "weixin",
      "token": "YOUR_WEIXIN_TOKEN",
      "allow_from": [
        "user_id_1",
        "user_id_2"
      ],
      "proxy": ""
    }
  }
}
```

### 字段解析

| 字段 | 说明 |
|---|---|
| `enabled` | 设置为 `true` 以在启动时激活该频道。 |
| `token` | 通过扫码获取的认证令牌。 |
| `allow_from` | (可选) 允许与机器人交互的微信 User ID 列表。如果为空，任何能给此微信号发消息的人都可以触发机器人。 |
| `proxy` | (可选) HTTP 代理地址（例如 `http://localhost:7890`），适合网络访问受限环境。 |

## 运行时状态存储

Weixin 游标和每用户上下文令牌以类型化记录存放在
`$PICOCLAW_HOME/channels/weixin/state.db`。首次使用时，会按确定顺序导入
`channels/weixin/sync/*.json` 和
`channels/weixin/context-tokens/*.json` 下的受限旧文件，并保留到
`channels/weixin/legacy-json/weixin-state-v1/`。PicoClaw 不会双写或新建可变
Weixin 状态 JSON。

## ⚠️ 注意事项

- **单端绑定**: iLink 令牌通常与单个会话绑定。在其他地方重新扫码激活可能会导致旧令牌失效。
- **频率控制**: 为避免触发微信的风控反垃圾机制，请避免设置死循环触发、高频广播等恶意行为。
