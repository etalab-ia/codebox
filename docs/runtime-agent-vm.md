# Runtime agent-vm

Le runtime `agent-vm` exécute le backend OpenCode dans une VM Linux Debian persistante pilotée par [Lima](https://lima-vm.io). Contrairement à Microsandbox (microVM éphémère par sandbox), la VM est persistante : les logiciels installés dans l'invité survivent aux arrêts.

Prérequis :

1. [Lima](https://lima-vm.io) (`brew install lima` sur macOS).
2. Rien d'autre : le template de base est construit par codebox lui-même au premier démarrage.

```bash
codebox doctor --agent-vm    # vérifie limactl et signale un template à construire
codebox --agent-vm           # construit le template si besoin, démarre la VM et attache le TUI
```

Au premier `start`, codebox construit un template de base nommé `agent-vm-base` (~5 minutes : paquets de base, Node.js 24, OpenCode), puis le clone en une VM nommée `opencode-agent-vm`, monte `WORKSPACE_DIR` à l'identique dans l'invité, démarre la VM et lance `opencode serve` dedans. Les démarrages suivants réutilisent le template. Avant le lancement, codebox vérifie OpenCode v2.2.5 et met à jour le CLI d'un invité géré en place si nécessaire, sans recréer son disque. Un template personnalisé incompatible est refusé avec une instruction de mise à jour. Le port 4096 est publié sur `127.0.0.1` côté hôte via le transfert de ports Lima ; les serveurs de dev lancés par l'agent sur les ports 3000-3010 sont également accessibles depuis l'hôte (transfert dynamique Lima). Les secrets (clé Albert, authentification HTTP) transitent par un fichier d'environnement poussé dans l'invité, jamais en ligne de commande.

Le provisionnement est volontairement minimal : les paquets de base, Node.js et OpenCode, plus un lien symbolique `opencode` dans `/usr/local/bin` — présent dans le `PATH` par défaut de tout shell, connecté ou non — pour que le backend trouve le binaire sans dépendre des fichiers d'initialisation d'un shell donné. `/etc/profile.d/codebox.sh` complète l'ensemble pour les shells interactifs. codebox ne construit ce template que sous son nom par défaut : tout autre `AGENT_VM_TEMPLATE` désigne un template que vous maintenez, et il n'est ni construit ni remplacé. C'est le point de personnalisation — une équipe peut y préinstaller Docker, Chromium ou d'autres agents et le désigner via `AGENT_VM_TEMPLATE` ; le clonage reste identique.

La construction est refaite si le template est absent **ou** s'il a été laissé inachevé. codebox écrit un marqueur d'achèvement sur l'hôte une fois le provisionnement et l'arrêt terminés : un template présent mais non marqué — le cas d'une construction interrompue, qu'un processus tué ne peut pas annuler lui-même — est reconstruit plutôt que cloné. Un template que vous maintenez n'a pas ce marqueur et est utilisé tel quel. Pour forcer une reconstruction (après un changement de ressources, par exemple), supprimez l'instance : `limactl delete agent-vm-base --force`.

Une opération interrompue est rattrapée : une construction qui échoue supprime le template partiel, pour qu'un nouvel essai reparte d'une base saine plutôt que de cloner une image sans OpenCode.

`codebox stop` arrête la VM (l'état est conservé), `codebox restart --agent-vm` l'arrête puis la relance sans effacer son disque, `codebox recreate --agent-vm` la reconstruit depuis le template (destructif), `codebox clean --agent-vm` la supprime avec son état local.
