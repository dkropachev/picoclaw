# 🐳 Docker et Démarrage Rapide

> Retour au [README](../project/README.fr.md)

## 🐳 Docker Compose

Vous pouvez également exécuter PicoClaw avec Docker Compose sans rien installer localement.

```bash
# 1. Cloner ce dépôt
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw

# 2. Démarrer le bundle mono-nœud Web + API + Gateway
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Ouvrez <http://localhost:18800/launcher-setup>, créez le mot de passe du dashboard, puis configurez un fournisseur et un modèle dans la WebUI.

Cette commande démarre un seul conteneur contenant :

- la WebUI intégrée et l'API du launcher sur le port publié `18800` ;
- le processus enfant Gateway géré par le launcher sur la boucle locale du conteneur ;
- les bases de données SQLite basées sur des fichiers et les données du workspace, conservées ensemble dans `docker/data/`.

Par défaut, le port `18800` est lié uniquement à l'adresse de boucle locale de l'hôte (`127.0.0.1`). Terminez `/launcher-setup` localement avant toute exposition. Uniquement si un accès LAN est demandé, relancez le bundle avec :

```bash
PICOCLAW_LAUNCHER_BIND=0.0.0.0 docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Le chat et les médias du navigateur passent par les proxies same-origin authentifiés du launcher ; il n'est donc pas nécessaire de publier le port du Gateway. Les sondes publiques `GET`/`HEAD` sur `/health` et `/ready` indiquent la disponibilité du launcher indépendamment de la configuration du Gateway ou du modèle.

> [!WARNING]
> La console web est protégée par un mot de passe de connexion au dashboard, mais elle ne termine pas TLS. Pour un accès LAN, utilisez un pare-feu ou un reverse proxy TLS et configurez les contrôles CIDR du launcher. Ne l'exposez pas directement à un réseau non fiable.

### Accès direct au Gateway (webhooks)

Si un canal de callback HTTP ou une intégration avancée nécessite un accès direct au Gateway, publiez explicitement le port `18790` avec le fichier complémentaire :

```bash
docker compose -f docker/docker-compose.yml \
  -f docker/docker-compose.gateway-public.yml up -d --remove-orphans
```

Le fichier complémentaire remplace l'adresse d'écoute du Gateway géré par `0.0.0.0` et publie le port `18790`. Protégez ce port avec un pare-feu ou un reverse proxy TLS. Les canaux utilisant une socket, un stream ou du long-polling n'ont pas besoin de ce fichier.

### Mode Gateway uniquement

Le fichier Compose de base ne contient que le launcher. Les services headless se trouvent dans `docker-compose.headless.yml`. Pour lancer uniquement le Gateway longue durée, ciblez explicitement son service et activez son profil :

```bash
docker compose -f docker/docker-compose.headless.yml --profile gateway up --remove-orphans picoclaw-gateway
```

`--remove-orphans` arrête tout conteneur launcher existant du même projet Compose avant le démarrage du Gateway autonome, empêchant ainsi deux arborescences de processus Gateway de partager le fichier PID et le répertoire SQLite.

Sur un volume neuf, l'image principale génère `docker/data/config.json` puis s'arrête. Renseignez les valeurs du fournisseur et des canaux, puis redémarrez ce service avec `-d`. Le mode Gateway uniquement sert les routes de webhook, Pico, de santé et d'exécution protégées sur le port du Gateway ; il ne fournit ni la WebUI du launcher ni des endpoints REST génériques de chat.

### Mode Agent (One-shot)

```bash
# Poser une question
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent -m "What is 2+2?"

# Mode interactif
docker compose -f docker/docker-compose.headless.yml run --rm picoclaw-agent
```

### Logs et arrêt

```bash
# Vérifier les logs du bundle par défaut
docker compose -f docker/docker-compose.yml logs -f picoclaw-launcher

# Arrêter le bundle
docker compose -f docker/docker-compose.yml down
```

### Mise à jour

```bash
docker compose -f docker/docker-compose.yml pull
docker compose -f docker/docker-compose.yml up -d --remove-orphans
```

Lors de la migration unique depuis l'ancienne disposition Compose basée sur des profils, arrêtez et supprimez ses conteneurs aux noms fixes avant le premier démarrage avec la nouvelle disposition. Cela ne supprime pas le répertoire `docker/data/` monté par bind mount :

```bash
docker stop picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
docker rm picoclaw-launcher picoclaw-gateway picoclaw-agent 2>/dev/null || true
```

### Sauvegarde et restauration

Arrêtez tous les processus PicoClaw avant de copier ou de restaurer `docker/data/`. Traitez le répertoire entier comme un seul snapshot afin que chaque base SQLite reste associée à ses fichiers `-wal`, `-shm` et de verrouillage correspondants. N'exécutez pas des versions différentes des binaires sur un même répertoire personnel/workspace.

### Runtime MCP complet

L'image du launcher par défaut est le runtime Alpine minimal et n'inclut ni Node.js, ni Python, ni `uv` pour les packages MCP stdio. `docker-compose.full.yml` conserve les profils existants de l'agent MCP complet et du Gateway headless ; il ne fournit actuellement aucun service launcher.

### 🚀 Démarrage Rapide

> [!TIP]
> Configurez votre clé API dans `~/.picoclaw/config.json`. Obtenir des clés API : [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). La recherche web est optionnelle — obtenez gratuitement une [API Tavily](https://tavily.com) (1000 requêtes gratuites/mois) ou une [API Brave Search](https://brave.com/search/api) (2000 requêtes gratuites/mois).

**1. Initialiser**

```bash
picoclaw onboard
```

**2. Configurer** (`~/.picoclaw/config.json`)

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

> **Nouveau** : Le format de configuration `model_list` permet l'ajout de fournisseurs sans modification de code. Voir [Configuration des Modèles](#configuration-des-modèles-model_list) pour plus de détails.
> `request_timeout` est optionnel et utilise les secondes. S'il est omis ou défini à `<= 0`, PicoClaw utilise le timeout par défaut (120s).

**3. Obtenir des clés API**

* **Fournisseur LLM** : [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Recherche Web** (optionnel) :
  * [Brave Search](https://brave.com/search/api) - Payant ($5/1000 requêtes, ~$5-6/mois)
  * [Perplexity](https://www.perplexity.ai) - Recherche alimentée par l'IA avec interface de chat
  * [SearXNG](https://github.com/searxng/searxng) - Métamoteur auto-hébergé (gratuit, pas de clé API nécessaire)
  * [Tavily](https://tavily.com) - Optimisé pour les agents IA (1000 requêtes/mois)
  * DuckDuckGo - Solution de repli intégrée (pas de clé API requise)

> **Note** : Voir `config.example.json` pour un modèle de configuration complet.

**4. Discuter**

```bash
picoclaw agent -m "What is 2+2?"
```

C'est tout ! Vous avez un assistant IA fonctionnel en 2 minutes.

---
