# Utilisation

## Commandes

```bash
codebox                        # démarre (RUNTIME) et attache le TUI
codebox --tart                 # démarre Tart et attache le TUI
codebox --microsandbox         # démarre Microsandbox et attache le TUI
codebox --agent-vm             # démarre agent-vm et attache le TUI
codebox start --tart           # démarre un backend sans attacher le TUI
codebox stop                   # arrête l'instance du projet courant
codebox stop --all             # arrête toutes les instances codebox
codebox check                  # santé du backend actif + provider Albert + ports forwardés (Microsandbox)
codebox logs --tart            # logs d'un runtime explicite
codebox shell --tart           # shell dans un runtime explicite
codebox restart --tart         # arrête puis relance l'instance existante
codebox recreate --tart        # recrée le sandbox (destructif, demande confirmation)
codebox clean --tart            # supprime le sandbox et son état local
codebox doctor --tart           # vérifie l'installation du runtime
codebox version                # identifie le binaire (version, commit, plateforme)
codebox ports list             # liste les previews configurées du projet
codebox ports add 8080:80       # publie le port invité 80 sur localhost:8080
codebox ports remove 8080       # retire cette redirection
codebox help                   # liste les commandes
```

Lancer `codebox` sans commande utilise Microsandbox/full par défaut et lance le TUI OpenCode dans l'invité. S'il existe déjà une session dans le projet, le TUI la reprend automatiquement (`opencode --continue`) ; au premier lancement, il s'ouvre normalement. `/new` démarre une nouvelle conversation. Si codebox ne peut pas vérifier l'existence de la session ou si le CLI OpenCode installé n'annonce pas `--continue`, il avertit et ouvre le TUI sans reprise plutôt que d'échouer. En mode backend explicite, le serveur invité écoute sur 4096 et le TUI hôte s'y attache ; ce port n'est pas transféré en mode full. Les previews Microsandbox utilisent par défaut 3000-3010 sur `127.0.0.1` ; la liste se configure par projet avec `codebox ports` (voir [Configuration](config.md)). Les commandes et les flags de runtime peuvent être donnés dans n'importe quel ordre (`codebox --microsandbox start` et `codebox start --microsandbox` sont équivalents). La commande `code` n'existe pas : la taper renvoie une erreur explicite.

Les environnements sont identifiés par projet (répertoire racine du worktree Git) : chaque projet a sa propre instance `cb-<nom>-<suffixe>`, et les commandes `stop`, `logs`, `shell`, `clean` et `check` ciblent le projet courant. `codebox stop` n'arrête que l'instance du projet courant ; `codebox stop --all` arrête toutes les instances codebox, tous runtimes confondus. Les instances legacy (singleton d'avant l'identification par projet) restent découvrables pour les opérations explicites et ne sont jamais renommées ni supprimées automatiquement.

Les runtimes ne doivent pas tourner simultanément si leurs ports de prévisualisation entrent en conflit. Le port serveur 4096 n'est transféré qu'en mode backend. Si un autre runtime est déjà actif, `codebox` (attachement), `codebox start` et `codebox restart` proposent de l'arrêter avant de continuer. Quand tu quittes le TUI OpenCode, l'attachement propose aussi d'arrêter le backend ; répondre non le laisse disponible pour une reconnexion. `codebox restart` arrête puis relance la même instance sans la recréer ; `codebox recreate` détruit et reconstruit l'environnement, perdant les sessions, les outils installés et les fichiers propres à l'invité. La recréation demande une confirmation interactive (refusée hors TTY, pour CI et scripts).

## Premier lancement Microsandbox

Dans un terminal interactif, le lancement affiche les étapes terminées : copie du projet, skills, MCP et disponibilité de l'agent. La copie indique les fichiers `.env` et les fichiers sensibles exclus. Les avertissements sont regroupés sur une seule ligne.

Si le projet contient des plugins ou des commandes MCP non approuvés, un menu propose **Approuver et démarrer**, **Voir le détail** ou **Quitter**. L'approbation reste sur le poste et porte sur le contenu présenté. Un fichier modifié exige une nouvelle approbation. Cela inclut le contenu des plugins locaux déclarés, y compris avec une URL `file://` : leurs chemins doivent rester dans le projet et ne pas utiliser de liens symboliques. Sans terminal interactif, le lancement reste bloqué : relire les entrées avec `codebox trust status`, puis les approuver explicitement avec `codebox trust approve`.

La durée complète d'un démarrage réussi, y compris la mise à jour de la configuration de l'instance, est enregistrée dans le répertoire d'état local (`launch-timings.json`). En mode backend, la mesure inclut l'attente de disponibilité de l'agent ; une attente qui échoue n'enregistre aucune durée. Le premier démarrage avec installation des outils et les démarrages suivants ont des mesures séparées. Sans mesure préalable, aucune durée chiffrée n'est annoncée. La première durée avec installation reste conservée, même après de nouveaux démarrages.

La version gérée est **OpenCode v2.2.5**, distribuée par `@opencode/cli` (et non par l'ancien paquet `opencode-ai`). En mode `full`, les sondes de session et le TUI utilisent `--standalone` : aucun service OpenCode autonome ne doit survivre au propriétaire du terminal. En mode `backend`, `opencode --server <url>` remplace `opencode attach`, avec un mot de passe transmis uniquement dans l'environnement (`OPENCODE_PASSWORD`) et l'identité fixe `opencode`. La santé utilise `/api/info` et les sessions l'API v2 paginée, limitée au projet de l'invité.

**Migration depuis v1.** Sur l'hôte, désinstaller `opencode-ai` avant d'installer `@opencode/cli@2.2.5` évite la collision npm sur le binaire `opencode`. Les VM Tart et agent-vm gérées sont mises à jour en place au lancement, sans supprimer leurs sessions ; un template agent-vm personnalisé doit fournir v2.2.5 lui-même. Les anciens sandboxes Microsandbox ont un script d'entrée immuable : codebox refuse de les réutiliser et demande `codebox recreate --microsandbox`. Exporter d'abord les sessions et les changements utiles : cette recréation est destructive et n'est jamais automatique. La reprise automatique concerne les sessions du magasin v2 ; codebox n'effectue pas de conversion des anciennes bases v1. Conserver des exports réalisés avec l'ancien CLI si leur historique doit être importé dans v2 (`opencode session import <fichier>`). `codebox shell` et `codebox stop` restent utilisables sur un ancien sandbox pour récupérer les données avant toute recréation. Les projets peuvent conserver leur JSONC v1 : OpenCode traduit les champs historiques ; codebox émet des providers et permissions natifs v2 et contrôle aussi les déclarations `plugins` et `mcp.servers` dans sa revue de confiance.

OpenCode conserve la version installée dans l'invité : la vérification de mise à jour est désactivée au lancement, quel que soit le runtime (Microsandbox, Tart, agent-vm), par la variable d'environnement `OPENCODE_DISABLE_AUTOUPDATE=1`. Le service de mise à jour v2 lit cette variable de processus ; codebox ne s'appuie pas sur un champ de configuration historique `autoupdate` et ne réécrit pas les fichiers globaux de l'utilisateur. `opencode upgrade` reste disponible pour une mise à jour volontaire.

## Niveaux d'isolation

`--isolation backend|full` (ou la variable d'environnement `ISOLATION`) choisit où vit l'agent :

```bash
codebox --microsandbox --isolation full    # tout l'agent tourne dans la microVM
codebox --tart --isolation backend         # comportement historique
```

En mode `full` (défaut sur un hôte/projet non configuré), le TUI lui-même tourne dans l'invité et l'hôte n'est qu'un passe-plat terminal ; `codebox check` rapporte alors l'état de la VM au lieu de sonder un endpoint de santé, qui n'existe pas dans ce mode. Le mode `backend` est un choix explicite : `opencode serve` tourne dans le sandbox et le TUI s'y attache depuis l'hôte ; seul le processus serveur est confiné, le TUI et les identifiants de connexion restent côté hôte. Sur le runtime Microsandbox, `check` sonde aussi chaque port explicitement configuré par le projet depuis l'hôte : toute réponse HTTP est une prévisualisation joignable, aucune réponse suggère le correctif côté invité (bind `0.0.0.0`). Les autres runtimes n'ont pas de forward de port hôte et ne sont pas sondés.

```bash
codebox check --isolation full             # état de la VM, pas de health check
```


## Répertoire de travail

Sans `WORKSPACE_DIR` explicite, la racine du projet courant est utilisée : Microsandbox en transfère une copie filtrée vers l'invité ; Tart et agent-vm la montent sur `/workspace`. Pour choisir une autre source/racine :

```bash
export WORKSPACE_DIR="$HOME/Code/mon-projet"
codebox
```

`PROJECT_DIR` reste accepté mais est déprécié (un avertissement le signale).
