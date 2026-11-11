```
               _      _
  ___ ___   __| | ___| |__   _____  __
 / __/ _ \ / _` |/ _ \ '_ \ / _ \ \/ /
| (_| (_) | (_| |  __/ |_) | (_) >  <
 \___\___/ \__,_|\___|_.__/ \___/_/\_\
```

[![CI](https://github.com/etalab-ia/codebox/actions/workflows/ci.yml/badge.svg)](https://github.com/etalab-ia/codebox/actions/workflows/ci.yml)
[![version](https://img.shields.io/github/v/release/etalab-ia/codebox?label=version&sort=semver)](https://github.com/etalab-ia/codebox/releases/latest)
[![go](https://img.shields.io/github/go-mod/go-version/etalab-ia/codebox?label=go)](https://go.dev/dl/)
[![licence](https://img.shields.io/github/license/etalab-ia/codebox?label=licence)](LICENSE)
![statut](https://img.shields.io/badge/statut-exp%C3%A9rimental-orange)
![plateformes](https://img.shields.io/badge/plateformes-darwin%20%7C%20linux%20%7C%20windows-blue)

⚠️ **PROJET EXPÉRIMENTAL : bac à sable de R&D pour évaluer l'exécution distante d'agents de code avec Albert API. Rien de stable, tout peut changer.** ⚠️

# codebox

**Un environnement OpenCode isolé dans une microVM Microsandbox, piloté par le TUI dans l'invité par défaut ou par le TUI hôte en mode backend explicite.**

Par défaut, l'agent et son TUI tournent dans la microVM Microsandbox avec les modèles souverains d'[Albert API](https://albert.api.etalab.gouv.fr). Le mode backend, Tart et agent-vm restent des choix explicites.

```text
full (défaut) : terminal hôte ──> TUI OpenCode dans l'invité
                                  /workspace = volume invité, fichiers transférés par filtre
                                  aucun port serveur OpenCode hôte (4096)

backend (explicite) : TUI hôte ──> 127.0.0.1:4096 ──> opencode serve dans l'invité
                                  previews invitées : 127.0.0.1:3000-3010
```

Ce n'est **pas un produit** : c'est un terrain de jeu pour mesurer l'UX (latence, reconnexion, persistance de session, previews web) et comparer les frontières d'isolation.

## Installation

Sur macOS ou Linux :

```sh
curl -fsSL https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.sh | sh
```

Dans PowerShell sous Windows :

```powershell
irm https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.ps1 | iex
```

L'installateur choisit une seule release stable, télécharge le binaire et `SHA256SUMS` depuis cette même release, puis compare l'empreinte avant de remplacer l'installation. Il annonce le résultat, traite `PATH` (en demandant l'autorisation avant de modifier un fichier de shell) puis lance l'assistant `codebox setup`.

Prérequis : une **clé d'au moins une plateforme d'inférence** — Albert API par défaut, Mistral AI ou une plateforme compatible OpenAI au choix (`codebox setup`). Le runtime Microsandbox est téléchargé et vérifié automatiquement au premier démarrage. En isolation complète (le défaut), OpenCode est installé dans l'invité : aucune installation sur l'hôte n'est nécessaire. En isolation partielle, l'hôte doit fournir l'OpenCode CLI (`npm uninstall -g opencode-ai && npm install -g @opencode/cli@2.2.5`).

Binaires par plateforme, installation manuelle, vérification, compilation depuis les sources : [docs/installation.md](docs/installation.md).

## Démarrage rapide

Pour un premier lancement, exporte la clé Albert API dans ton terminal.

macOS / Linux :

```bash
export ALBERT_API_KEY="ta-clé"
codebox
```

Windows (PowerShell) :

```powershell
$env:ALBERT_API_KEY = "ta-clé"
codebox
```

`codebox` sans argument utilise **Microsandbox en isolation `full`** : l'agent, sa TUI et ses identifiants tournent dans la microVM, dont l'espace de travail est scellé (aucun fichier de l'hôte n'y est monté). `--tart` et `--agent-vm` restent disponibles explicitement, par flag ou via `RUNTIME`.

La commande prépare l'espace de travail de l'invité, démarre l'agent et attache la TUI OpenCode native. Le premier démarrage d'un sandbox Microsandbox installe ~384 Mio de paquets dans la microVM et peut dépasser largement une minute ; les démarrages suivants sont rapides.

Pour rendre la clé persistante et enregistrer le runtime, le workspace ou d'autres réglages, consulte la page [Configuration](docs/config.md).

Une fois attaché, ces prompts exercent les dimensions clés de l'expérience :

1. **« Qu'y a-t-il dans ce workspace ? Décris la structure du projet. »** — inférence et outils fichiers de base.
2. **« Sur quel OS, noyau et architecture tournes-tu ? »** — confirme que l'agent s'exécute dans le sandbox Linux, pas directement sur ton Mac.
3. **« Code un petit jeu snake servi par un serveur Node sur le port 3000, puis lance-le. »** — écriture de fichiers + serveur de dev ; ouvre `http://localhost:3000` pour vérifier la preview.

Quatre autres prompts (toolchain polyglotte, édition et revue de diff, continuité de session, scratch space) : [docs/quickstart.md](docs/quickstart.md).

## Documentation

| Page | Contenu |
|---|---|
| [Installation](docs/installation.md) | Binaires, installateurs, vérification, dépendances de l'hôte, compilation depuis les sources |
| [Démarrage rapide](docs/quickstart.md) | Premier lancement et prompts d'exemple complets |
| [Configuration](docs/config.md) | Assistant `setup`, variables d'environnement, fichiers gérés, redirections de ports par projet, skills, MCP, modèle OpenCode |
| [Identifiants et secrets](docs/credentials.md) | Magasin d'identifiants, résolution de la clé Albert, protection par runtime, GitHub dans l'invité |
| [Statistiques d'usage](docs/stats.md) | Consentement, commandes `stats`, données partagées et jamais collectées, envoi non bloquant |
| [Sécurité du workspace](docs/workspace-security.md) | Workspace scellé, filtre de transfert, export des changements |
| [Utilisation](docs/usage.md) | Commandes, instances par projet, niveaux d'isolation, répertoire de travail |
| [Runtime Microsandbox](docs/runtime-microsandbox.md) | Téléchargement vérifié, mode manuel (`MSB_PATH`), prérequis plateforme |
| [Plateformes](docs/plateformes.md) | Plateformes prises en charge, magasins d'identifiants, et ce qui est réellement qualifié |
| [Runtime Tart](docs/runtime-tart.md) | Réseau et MTU, images et nommage des VM |
| [Runtime agent-vm](docs/runtime-agent-vm.md) | VM Lima persistante, template de base |
| [Dépannage](docs/troubleshooting.md) | Backend jamais healthy, conteneur Docker hérité |
| [Choix de conception](docs/design.md) | Décisions structurantes et documents de décision |
| [Développement](docs/development.md) | Architecture interne, hooks pre-commit, CI et publication |
| [Qualification bêta](docs/qualification-beta.md) | Protocole de test manuel de la version bêta |

## Contribuer

L'architecture du portage Go, les hooks pre-commit (gitleaks) et le pipeline d'intégration continue et de publication (release-please) sont documentés dans [docs/development.md](docs/development.md). La résolution typée de la configuration (sources, précédence, schémas des fichiers gérés, transition depuis `.env`) est documentée dans [docs/config.md](docs/config.md).

---

codebox · département IA dans l'État (IAE), DINUM · Licence MIT
