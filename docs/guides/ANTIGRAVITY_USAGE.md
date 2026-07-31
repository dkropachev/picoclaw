# Using Antigravity Provider in PicoClaw

This guide explains how to set up and use the **Antigravity** (Google Cloud Code Assist) provider in PicoClaw.

## Prerequisites

1.  A Google account.
2.  Google Cloud Code Assist enabled (usually available via the "Gemini for Google Cloud" onboarding).

## 1. Authentication

To authenticate with Antigravity, run the following command:

```bash
picoclaw auth login --provider antigravity
```

### Manual Authentication (Headless/VPS)
If you are running on a server (Coolify/Docker) and cannot reach `localhost`, follow these steps:
1.  Run the command above.
2.  Copy the URL provided and open it in your local browser.
3.  Complete the login.
4.  Your browser will redirect to a `localhost:51121` URL (which will fail to load).
5.  **Copy that final URL** from your browser's address bar.
6.  **Paste it back into the terminal** where PicoClaw is waiting.

PicoClaw will extract the authorization code and complete the process automatically.

## 2. Managing Models

### List Available Models
To see which concrete model IDs your project has access to and check their
quotas:

```bash
picoclaw auth models
```

Discovery does not select a model. Create an exact alias and pair it with an
account:

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "antigravity-main",
      "provider": "antigravity",
      "model": "",
      "auth_method": "oauth"
    }
  ],
  "model_aliases": [
    {
      "name": "chat",
      "model": "claude-opus-4-6-thinking"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "antigravity-main",
      "model_name": "chat"
    }
  }
}
```

### Switch Aliases

Change the configured account/alias pair in `~/.picoclaw/config.json`, or
override the alias for one CLI call:

```bash
# `chat` must already exist in model_aliases[]
picoclaw agent -m "Hello" --model chat
```

## 3. Real-world Usage (Coolify/Docker)

If you are deploying via Coolify or Docker, follow these steps to test:

1.  **Environment Variables**:
    * `PICOCLAW_AGENTS_DEFAULTS_ACCOUNT_REF=antigravity-main`
    * `PICOCLAW_AGENTS_DEFAULTS_MODEL=chat`
2.  **Authentication persistence**: 
    If you've logged in locally, you can copy your credentials to the server:
    ```bash
    scp ~/.picoclaw/auth.json user@your-server:~/.picoclaw/
    ```
    *Alternatively*, run the `auth login` command once on the server if you have terminal access.

## 4. Troubleshooting

*   **Empty Response**: If a model returns an empty reply, it may be restricted for your project. Try `gemini-3-flash` or `claude-opus-4-6-thinking`.
*   **429 Rate Limit**: Antigravity has strict quotas. PicoClaw will display the "reset time" in the error message if you hit a limit.
* **404 Not Found**: Ensure the concrete model stored in your selected alias is
  present in `picoclaw auth models`. Use the short ID (for example
  `gemini-3-flash`) rather than the full path.

## 5. Summary of Working Models

Based on testing, the following models are most reliable:
*   `gemini-3-flash` (Fast, highly available)
*   `gemini-2.5-flash-lite` (Lightweight)
*   `claude-opus-4-6-thinking` (Powerful, includes reasoning)
