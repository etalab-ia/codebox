# Configuration

Ce document décrit la configuration de just-code : assistant `setup`, sources
et précédence, variables d'environnement, fichiers gérés, skills et MCP,
modèle OpenCode, et transition depuis la configuration legacy `.env`. La
[sécurité du workspace](workspace-security.md) et la
[protection des identifiants](credentials.md) sont traitées dans des pages
dédiées.

## Principe

La configuration est résolue champ par champ, sans mutation de l'environnement du
processus et sans lecture implicite de fichier. Chaque champ porte sa
provenance. Les secrets n'entrent jamais dans les fichiers gérés : les
identifiants sont référencés par nom (`credentialRef`) et stockés séparément
(voir [Identifiants et secrets](credentials.md)).

## Précédence (champs non secrets)

Du plus fort au plus faible :

1. Flags explicites (`--runtime`, etc.)
2. Environnement nommé (`JUST_CODE_*`)
3. Manifeste projet (`.just-code/project.json`)
4. Réglages utilisateur globaux (`settings.json`)
5. Valeurs intégrées par défaut

Une valeur explicitement vide gagne sur toute source inférieure : elle signifie
« désactivé », comme le mot de passe serveur vide dans le chemin legacy. La
présence d'une valeur est distinguée de son absence (`Set`).

Seules les variables nommées `JUST_CODE_*` sont lues par le nouveau résolveur.
Les variables legacy non préfixées (`RUNTIME`, `ISOLATION`, `WORKSPACE_DIR`,
`ALBERT_API_KEY`, …) conservent leur sens documenté pendant l'intervalle de
dépréciation, mais n'entrent pas dans la nouvelle résolution.

La résolution des clés au démarrage (`credentialRef` pour Albert, variable
d'environnement de la plateforme, magasin d'identifiants) est détaillée dans
[Identifiants et secrets](credentials.md). Pour Mistral AI et une plateforme
compatible OpenAI, la variable lue est respectivement `MISTRAL_API_KEY` et
`OPENAI_COMPATIBLE_API_KEY` ; `credentialRef` ne s'applique qu'à Albert, pour
qu'une référence souveraine ne puisse pas alimenter une autre plateforme.

## Fichiers gérés

| Fichier | Rôle | Version de schéma |
|---|---|---|
| `~/.config/just-code/settings.json` | Réglages globaux utilisateur (sans secrets) | 1 |
| `.just-code/project.json` | Manifeste projet (sans secrets, sans chemins absolus hôte) | 6 |
| `.just-code/lock.json` | Verrou : révisions et digests des éléments épinglés | 3 |

`settings.json` conserve aussi `statsEnabled` et `statsConsentAsked` : l'état
du consentement aux statistiques d'usage, jamais une donnée d'usage elle-même.
La file d'événements vit dans l'état local de l'hôte, pas dans les réglages.

## Ports publiés Microsandbox

`just-code ports list` affiche les redirections TCP configurées. Les commandes
`just-code ports add 8080` et `just-code ports add 8080:80` ajoutent une
redirection de `127.0.0.1:8080` vers le port invité 8080 ou 80 ;
`just-code ports remove 8080` la supprime. Les ports sont inscrits dans
`.just-code/project.json`, sans chemin hôte ni secret. Le port 4096 est réservé
au backend OpenCode.

Les manifestes sans liste explicite conservent les redirections historiques
3000-3010. Une liste explicite remplace cette plage, y compris `[]` pour ne
publier aucun port de preview. Les redirections sont TCP et liées à
`127.0.0.1`, jamais à toutes les interfaces.

Microsandbox fixe ses ports publiés à la création du sandbox. Une modification
du manifeste n'est donc pas appliquée par `restart`; `just-code ports add` et
`remove` affichent la commande explicite `just-code recreate --microsandbox`.
Cette recréation détruit les sessions, outils installés et fichiers propres à
l'invité ; confirmer la perte avant de l'exécuter.

## Configurer la machine (`setup`)

`just-code setup` prépare la machine en une passe guidée : diagnostic
(plateforme, virtualisation, disque — lecture seule) → écran d'introduction
(parcours annoncé, Entrée pour commencer) → plateformes d'inférence (Albert
API, Mistral AI, plateforme compatible OpenAI ; Albert pré-cochée, au moins une
exigée) → une clé par plateforme (masquée, validée contre la plateforme ; rejet,
indisponibilité réseau et réponse non compatible distincts) → sélection des
modèles groupés par plateforme et d'un seul modèle par défaut → identifiant
GitHub optionnel → identité git → consentement aux statistiques d'usage → revue
→ application (réglages globaux + runtime managé). Le consentement et les
données concernées sont décrits dans [Statistiques d'usage](stats.md).

L'ordre suit celui annoncé par l'écran d'introduction : plateformes et clés,
modèles, puis GitHub. Pour Albert API, l'écran présente les quatre modèles
retenus et pré-sélectionne Qwen3 Coder et DeepSeek V4 Flash ; pour Mistral AI et
une plateforme compatible OpenAI, il lit le catalogue brut de la plateforme à
l'exécution. La disponibilité Albert vient du catalogue Albert API ; un modèle
absent est marqué indisponible et ne peut pas être choisi. Si un catalogue est
injoignable, la sélection actuelle est conservée. Les modèles cochés sont
enregistrés dans les réglages utilisateur et sont proposés par `/models` dans
OpenCode. Un seul modèle est par défaut, toutes plateformes confondues. Si
`JUST_CODE_MODEL` ou le manifeste du projet impose un autre modèle, ce modèle
est aussi exposé pour que le lancement reste possible. Le choix par défaut peut
ensuite être modifié avec `just-code setup`.

OpenCode exige que chaque objet `limit` définisse à la fois `context` et
`output`. just-code n'écrit cet objet que lorsque les deux valeurs sont
connues. Si l'une manque dans les métadonnées du modèle, il omet tout l'objet :
aucune limite n'est inventée, et les valeurs par défaut d'OpenCode s'appliquent.

Les plateformes connectées sont enregistrées dans `settings.json`
(`providers`) : leur type, leur nom et, pour une plateforme compatible OpenAI,
son URL de base. Les clés restent dans le magasin d'identifiants, jamais dans ce
fichier. Albert API et Mistral AI ont une URL fixe : une URL personnalisée est
refusée pour que la clé ne puisse pas être envoyée à un autre hôte. Pour une
plateforme compatible OpenAI, l'URL doit être en `https`, sans identifiants,
requête, fragment ni port non standard ; son hôte devient la seule destination
autorisée pour la clé. Les hôtes des plateformes configurées doivent être
disjoints.

```bash
just-code setup            # assistant interactif
just-code setup doctor     # rapport lecture seule, aucune écriture
just-code setup doctor --json # même rapport, JSON pour automatisation
just-code setup --fallback # utiliser le magasin fichier consentit
just-code setup --no-color # sortie terminal simple
```

`just-code setup doctor` rend un rapport séparé par sections : capacité de
l'hôte, runtime et état de l'instance du projet, toolchain hôte, vérification
Albert, skills et MCP du projet, et stockage des identifiants. Sur un terminal
compatible, il utilise le rendu TUI ; avec `--no-color` ou une sortie non TTY,
la même information apparaît en texte stable. Les valeurs d'identifiants ne
sont jamais affichées.

Règles du flux :

- **Aucune saisie avant un bloqueur fatal.** La virtualisation et le disque
  sont vérifiés avant toute invite : la clé n'est jamais demandée sur une
  machine qui ne peut pas exécuter le runtime.
- **Introduction sur un premier lancement.** Un setup neuf affiche l'écran
  d'introduction (bannière, durée estimée, les trois étapes) et attend Entrée
  avant la première question. Un setup repris ne le réaffiche pas. L'écran
  n'est montré que si l'entrée et la sortie sont toutes deux un terminal : sur
  une exécution non interactive, il est omis plutôt que d'attendre une touche
  qui ne viendra pas.
- **Reprise.** Un setup interrompu reprend là où il s'est arrêté (journal
  d'état hôte) — les identifiants déjà stockés ne sont pas redemandés.
- **Rejet ≠ réseau.** Une clé refusée (401/403) est signalée et **redemandée
  sur place**, avec le lien de création de clé : un refus ne fait jamais
  quitter l'assistant. Un endpoint injoignable laisse le choix entre réessayer
  (`R`) et continuer sans vérifier (`C`) ; en continuant, la clé est stockée
  avec un avertissement et la validation se rattrapera avec
  `just-code models`. Aucun appel d'inférence n'est effectué pour valider.
- **GitHub sans pénalité.** Ignorer l'identifiant GitHub ne bloque rien ;
  un identifiant stocké reste inactif jusqu'à l'activation explicite du
  workflow GitHub pour un projet Microsandbox.
- **Magasin indisponible = choix explicite.** Un Secret Service absent
  propose le repli fichier consentit (`--fallback`), jamais une création
  silencieuse.

**Un fichier `.env` n'est pas lu.** just-code lit l'environnement du processus : exporte les variables dans ton shell (ou dans le gestionnaire de secrets de ton choix), et passe les réglages ponctuels par des flags.

```bash
export ALBERT_API_KEY=ta-clé          # ou : just-code auth add albert
just-code                             # Microsandbox, isolation full, racine du projet
```

C'est volontaire : un `.env` non versionné dans le répertoire courant rendait le lancement dépendant d'un fichier invisible (et de son contenu en secrets), et deux invocations du même binaire pouvaient se comporter différemment. Si tu as un `.env` d'une installation précédente, just-code te le signale au lancement ; pour voir ce qu'il contient et où chaque valeur va :

```bash
just-code config import-env .env      # prévisualisation seule : rien n'est écrit
just-code auth add albert             # la clé va dans le magasin d'identifiants
```

L'application automatique d'un import n'existe pas encore : la prévisualisation liste les valeurs reconnues et le fichier d'origine n'est jamais modifié.

Un `.env.example` reste dans le dépôt comme **gabarit documentaire** : il liste les variables et leurs défauts, il n'est jamais lu.

## Variables d'environnement

| Variable | Requise | Valeur par défaut | Rôle |
| --- | --- | --- | --- |
| `ALBERT_API_KEY` | oui | aucune | Clé utilisée par le provider Albert API. Ne la commite jamais. |
| `RUNTIME` | non | `microsandbox` | Runtime préféré : `microsandbox`, `tart` ou `agent-vm`. Un flag explicite reste prioritaire. Microsandbox est le défaut sur toutes les plateformes, parce que c'est le runtime à espace de travail scellé. |
| `ISOLATION` | non | `full` | Frontière d'exécution de l'agent : `full` (tout l'agent, TUI et identifiants compris, tourne dans l'invité) ou `backend` (le serveur tourne dans le sandbox et le TUI s'y attache depuis l'hôte). Le défaut est `full` : le parcours à zéro option est celui où l'agent reste dans le sandbox. Un flag explicite reste prioritaire. |
| `WORKSPACE_DIR` | non | racine du projet | Répertoire de travail du projet. En Microsandbox c'est la **source** du contenu transféré, filtré, vers l'invité (voir [Sécurité du workspace](workspace-security.md)) ; pour Tart et agent-vm c'est le répertoire **monté** sur `/workspace`. Un chemin relatif est résolu depuis le répertoire de lancement. Un `WORKSPACE_DIR` explicite est toujours honoré tel quel. |
| `PROJECT_DIR` | non | — | Ancien nom de `WORKSPACE_DIR`, encore accepté avec un avertissement. Ne pas utiliser dans une nouvelle configuration. |
| `OPENCODE_SERVER_USERNAME` | non | `opencode` | Identité fixe d'OpenCode v2 : seule la valeur `opencode` est acceptée au démarrage en mode backend. |
| `OPENCODE_SERVER_PASSWORD` | non | `albert-dev-pass` | Mot de passe HTTP du backend. OpenCode v2 exige une valeur non vide en mode backend ; la valeur est transmise aux processus par `OPENCODE_PASSWORD`, jamais par les arguments. Une variable `OPENCODE_PASSWORD` héritée ne remplace pas ce réglage just-code. |
| `JUST_CODE_HARNESS` | non | `opencode` | Harnais de l'agent exécuté dans l'invité : `opencode` ou `pi`. Prioritaire sur le manifeste du projet. Un harnais inconnu est refusé au lancement plutôt que remplacé silencieusement. |
| `JUST_CODE_START_TIMEOUT` | non | `300` | Délai maximal, en secondes entières positives, pour attendre que le backend soit prêt avant d'attacher le TUI. |
| `JUST_CODE_CPUS` | non | `2` (réduit à `1` sur un hôte de deux CPU ou moins) | Nombre de processeurs alloués à l'invité Microsandbox, jusqu'au nombre de processeurs logiques détectés sur l'hôte. L'utilisation de tous les CPU déclenche un avertissement ; une valeur supérieure est refusée. Si un seul CPU est disponible, il est sélectionné automatiquement. Prioritaire sur le manifeste du projet. |
| `JUST_CODE_MEMORY_MB` | non | `4096` Mio (abaissé pour laisser 25 % de mémoire à l'hôte si nécessaire) | Mémoire allouée à l'invité Microsandbox. Accepte un nombre entier en Mio ou un nombre en Gio, par exemple `4096`, `4G` ou `2.5G` (`G` = 1024 Mio). Le plafond est la mémoire hôte détectée ; une valeur supérieure est refusée. Un avertissement est affiché à partir de 80 % de la mémoire hôte. Prioritaire sur le manifeste du projet. |
| `MSB_HOME` | non | `~/.microsandbox` | Racine du runtime et de l'état Microsandbox gérés. |
| `MSB_PATH` | non | runtime géré | Chemin direct vers un binaire `msb` fourni manuellement. À définir avec `MSB_LIBKRUNFW_PATH`. |
| `MSB_LIBKRUNFW_PATH` | non | runtime géré | Chemin direct vers la bibliothèque `libkrunfw` fournie manuellement. À définir avec `MSB_PATH`. |
| `TART_IMAGE` | non | `ghcr.io/cirruslabs/macos-tahoe-base:latest` | Image utilisée pour créer la VM Tart. Sans effet sur Microsandbox. |
| `TART_MTU` | non | `1280` | MTU de l'invité Tart : entier de `1280` à `1500`, ou `auto` pour ne pas la modifier. Sans effet sur Microsandbox. |
| `AGENT_VM_TEMPLATE` | non | `agent-vm-base` | Template Lima servant de base à la VM agent-vm. Sous ce nom par défaut, just-code le construit s'il est absent ; tout autre nom désigne un template que vous maintenez, qui n'est jamais construit ni remplacé. Sans effet sur les autres runtimes. |
| `AGENT_VM_VM` | non | `opencode-agent-vm` | Nom de la VM Lima gérée par just-code. Sans effet sur les autres runtimes. |
| `AGENT_VM_IMAGE` | non | `template://debian-12` | Image Lima utilisée pour créer le template de base. La valeur par défaut est épinglée sur une version précise, car l'alias `template://debian` suit le Debian courant et changerait l'invité lors d'une mise à jour de Lima. Pour désigner un template intégré à Lima, la forme dépend de la version de Lima : jusqu'à la 1.x, seules les deux barres obliques fonctionnent (`template:debian-12` échoue avec « identifier must not be empty ») ; depuis la 2.0, les deux écritures sont acceptées. Les autres emplacements acceptés par `limactl create` fonctionnent tels quels : URL `https://`, `file://` ou chemin local. L'image est vérifiée avant la reconstruction du template de base, donc avant que l'ancien template ne soit supprimé ; `agent-vm doctor` la vérifie aussi. La vérification est ignorée si votre `limactl` est antérieur à 1.1.0, qui ne sait pas valider un emplacement. Sans effet sur les autres runtimes. |
| `AGENT_VM_DISK_GB` | non | `20` | Taille du disque du template de base, en Go. Lima ne sait agrandir un disque que dans ce sens : prévoir large. Sans effet sur les autres runtimes. |
| `AGENT_VM_MEMORY_GB` | non | `4` | Mémoire du template de base, en Go. Sans effet sur les autres runtimes. |
| `AGENT_VM_CPUS` | non | `2` | Nombre de processeurs du template de base. Sans effet sur les autres runtimes. |

## Priorité et prise d'effet

- **Le `.env` n'est plus lu.** Le lancement dépendait d'un fichier non versionné du répertoire courant — exactement le genre de fichier qui porte des secrets et que l'espace scellé existe pour tenir hors de l'invité — et deux invocations du même binaire pouvaient se comporter différemment. Les variables **exportées** sont lues ; un `.env` présent est signalé avec la commande qui l'adopte (`just-code config import-env <chemin>`).
- `--microsandbox`, `--tart` ou `--agent-vm` prime sur `RUNTIME`.
- `--isolation backend|full` prime sur `ISOLATION` ; un flag explicite reste utilisable même si `ISOLATION` contient une valeur invalide.
- Sans `WORKSPACE_DIR`, le **répertoire de travail est la racine du projet**. Pour Tart et agent-vm, cela change ce qui est monté : le montage devient le projet lui-même au lieu de `./workspace`, et donc la porte de sécurité de ces runtimes (qui refusent un workspace contenant un `.env`, puisqu'ils le montent) porte désormais sur le projet. En Microsandbox il n'y a pas de montage : changer la source prend effet au `workspace sync` suivant, sans recréation.
- Le **dimensionnement de l'invité** (`JUST_CODE_CPUS`, `JUST_CODE_MEMORY_MB`) est figé à la création du sandbox : le runtime ne redimensionne pas un invité en cours. Une valeur enregistrée dans le manifeste du projet qui change est donc signalée comme nécessitant une recréation, jamais ignorée en silence.
- Le dimensionnement CPU et mémoire est plafonné à **100 % de la capacité hôte détectée** pendant l'initialisation et avant chaque lancement, y compris pour les valeurs provenant d'un manifeste ou de variables d'environnement. La revue avertit si l'invité utilise tous les processeurs logiques ou au moins 80 % de la mémoire hôte ; Apply confirme ces choix. Un parcours non interactif à risque exige `--yes`. Si un seul CPU entier est disponible, il est choisi automatiquement et le TUI l'indique.
- Le niveau d'isolation est figé à la création du sandbox Microsandbox : ses scripts de démarrage sont persistés et ne peuvent pas être réécrits. Basculer `ISOLATION` sur un sandbox existant est refusé avec un message ; `just-code recreate --microsandbox` le recrée dans le mode demandé.
- Une modification des identifiants HTTP nécessite `just-code stop`, puis un nouveau lancement pour redémarrer le backend avec les nouvelles valeurs.
- Une modification de `TART_MTU` nécessite `just-code stop`, puis `just-code --tart`. Elle ne nécessite pas de recréer la VM.
- Changer `TART_IMAGE` cible une autre VM Tart ; les VM créées depuis des images différentes peuvent coexister.

Les réglages propres à Tart sont détaillés dans [Runtime Tart](runtime-tart.md). Les montages de workspace et leur recréation sont expliqués dans [Utilisation](usage.md).

## Skills de projet

Les skills sélectionnés pendant `just-code init` sont épinglés à une révision
du dépôt `etalab-ia/skills`. En mode versionné, les IDs sont dans
`.just-code/project.json`, et le commit source avec le SHA-256 de chaque
archive sont dans `.just-code/lock.json`. Chaque archive inclut la licence
amont MIT. Les archives vérifiées sont conservées dans le cache utilisateur :
le lancement lit uniquement le lock et ce cache, sans
suivre une branche distante ni exécuter d'installateur côté hôte.

Dans l'invité Microsandbox, les fichiers sont installés sous le répertoire
skills global d'OpenCode (`$XDG_CONFIG_HOME/opencode/skills`, ou
`~/.config/opencode/skills`). Les skills restent hors du checkout invité; une
collision avec un skill existant est refusée sans l'écraser. Les archives sont
limitées aux fichiers réguliers : symlinks, chemins traversants, doublons,
archives corrompues et contenu modifié après installation font échouer la
préparation.

La commande interactive `init` présente le catalogue officiel et les entrées
expérimentales explicitement marquées. En script, répéter `--skill` pour chaque
sélection :

```bash
just-code init --root . --skill official/rgaa --skill official/anssi-guides --yes
just-code init --root . --skill experimental/rag-parse --yes
just-code init --root . --clear-skills --replace --yes
```

La désélection se fait avec
`--clear-skills`. Le mode versionné ajoute uniquement une zone bornée gérée
dans `AGENTS.md`; tout le texte hors de cette zone est préservé. Sur le
runtime Microsandbox, cette zone existe même sans skill sélectionné : elle
porte des instructions permanentes (notamment le bind `0.0.0.0` requis côté
invité pour qu'un serveur de développement soit joignable via le forward de
port hôte). Sur les autres runtimes, qui n'ont pas ce forward, la zone ne
contient que les skills épinglés et n'est pas créée sans sélection. Avec
`--local-only-skills`, IDs et pins sont stockés dans l'état hôte de l'instance,
et aucun texte de skill n'est ajouté au checkout. Si le projet avait auparavant
une zone gérée versionnée dans `AGENTS.md`, son retrait est montré avant Apply;
le texte utilisateur hors de cette zone reste intact. Pour changer la
sélection ou les pins après création du guest, recrée explicitement
l'environnement après avoir exporté les éventuels changements invités.
Le passage du mode versionné au mode local-only ne rafraîchit pas la copie
scellée d'`AGENTS.md` du guest. Si elle porte encore des épingles de skills,
`start` et `attach` refusent de poursuivre : exporte les changements invités,
puis lance `just-code workspace sync` ou recrée le guest. La seule présence
des instructions permanentes sans épingles ne bloque rien.

Chaque fichier porte un `schemaVersion`. Un schéma plus récent que la version
supportée par le binaire est une erreur explicite (mettre à jour just-code), pas
une lecture approximative. Les champs à allure de secret (`apiKey`, `token`,
`password`, `secret`, `credentials`) sont rejetés au parse : seule la forme
`credentialRef` est acceptée. Les écritures sont atomiques (temporaire + rename).

La valeur référencée par `credentialRef` vit dans le magasin natif de l'OS
(Keychain, Gestionnaire d'identifiants, Secret Service), géré par
`just-code auth` (voir [Identifiants et secrets](credentials.md)). Sur les hôtes sans magasin natif, un repli fichier
`0600` existe mais n'est jamais créé sans consentement explicite
(`just-code auth add --fallback`).

## Mises à jour projet

`just-code update` compare les skills versionnés du projet avec la révision
HEAD du catalogue officiel. Les mises à jour sont explicites et révisables ;
elles ne s'appliquent jamais au lancement :

```bash
just-code update
just-code update --skill official/rgaa
just-code update --yes  # approbation explicite non interactive
```

La commande affiche les révisions et digests
proposés, puis demande confirmation ; `--skill <id>` répété limite la sélection.
Sans TTY, elle reste en aperçu sauf si `--yes` approuve explicitement toutes
les mises à jour affichées. Une panne réseau ou une archive invalide ne modifie
ni le manifeste, ni le lock, ni les instructions gérées. La résolution peut
alimenter le cache utilisateur avant confirmation ; aucun fichier projet n'est
écrit avant l'approbation.

Les archives sont adressées par révision et SHA-256 dans le cache utilisateur.
Le cache ne les purge pas automatiquement : d'anciens locks doivent rester
utilisables hors ligne. Les entrées devenues inutiles ne peuvent être supprimées
qu'après vérification qu'aucun projet ne référence encore leur révision.

Après confirmation, le manifeste et le lock reçoivent le même identifiant de
génération. Un journal temporaire `.just-code/update-journal.json` permet de
restaurer l'ancienne paire si l'écriture s'interrompt ; le prochain `start`,
`init` ou `update` récupère le journal avant de lire la configuration. Il ne
contient que les snapshots validés du manifeste et du lock, jamais les octets
d'`AGENTS.md`. La zone de skills gérée est recalculée en préservant le texte
utilisateur hors marqueurs. Un lancement refuse toute paire dont les
identifiants de génération divergent.

Si le manifeste ou le lock contient des octets qui ne correspondent ni à
l'ancienne ni à la nouvelle version du journal, la récupération automatique
refuse d'écraser ces modifications et nomme le journal concerné. Après examen,
`just-code update --recover --rollback` restaure la paire précédente et recalcule
uniquement la zone gérée dans `AGENTS.md`. Les copies des fichiers gérés présents
sont conservées dans `.just-code/recovery-backups/`. En mode non interactif,
ajouter `--yes` pour approuver explicitement le rollback.

Cette commande met à jour les dépendances projet, pas le binaire just-code.
L'image de base Microsandbox est épinglée au digest OCI
`sha256:b34342987ca889fc2cc19cbc046eefc2418e5980a3d696e209fbb401a288f631` ;
les MCP distants sont des services HTTP sans révision immuable exposée. Les
images et outils livrés avec une version du CLI se mettent à jour via une
version ultérieure du CLI, pas silencieusement au lancement. Lors d'une mise à
niveau depuis just-code 0.7.0, un invité existant créé avec l'ancienne référence
flottante peut demander une recréation explicite. Celle-ci détruit ses sessions,
outils installés et fichiers invités : synchroniser ou exporter le travail
avant de confirmer.

## Importer un projet Albert Code

`just-code import albert-code` détecte les artefacts de setup Albert Code et
affiche un aperçu en lecture seule :

```bash
just-code import albert-code
just-code import albert-code --root ../mon-projet
```

`--root <chemin>` choisit le projet ;
`--apply` approuve l'écriture du manifeste et du lock just-code. L'import
refuse de remplacer un manifeste just-code différent et une répétition sur un
projet déjà importé est un no-op. La racine doit être un worktree Git.

L'import ne retient que les skills officiels présents dans le catalogue épinglé,
un modèle associé au provider Albert standard et les MCP intégrés dont la
définition correspond exactement aux endpoints/commandes attendus. Les
réglages personnalisés et inconnus sont nommés sans afficher leurs valeurs,
puis restent dans les fichiers source. Les markers Albert Code d'`AGENTS.md`
sont reconnus ; une paire invalide est signalée. Si des skills sont importés,
Init ajoute uniquement sa propre zone gérée, sans remplacer le texte existant
ni la zone Albert Code.

Les fichiers OpenCode, `.albert-code/skills.txt`, `.env`, le script
`.agent-vm.runtime.sh` et les profils shell ne sont pas exécutés ou réécrits.
Le script runtime et les profils ne sont même pas lus. Aucun changement n'est
apporté à `.git/info/exclude`. Le VM, les sessions, fichiers invités non
suivis et outils installés restent sur l'ancien hôte. Le nouveau guest reçoit
un clone filtré par le transfert default-deny ; `.env` est exclu.

Les fichiers source symlinkés ou non réguliers sont ignorés avec un avertissement ;
les dossiers `.opencode` et `.albert-code` symlinkés sont refusés. Le parseur
JSONC partagé avec la détection des conflits OpenCode rejette les commentaires
incomplets et les documents qui ne sont pas des objets JSON, au lieu d'en
accepter silencieusement un préfixe.

L'import de clé est une approbation distincte :
`just-code import albert-code --apply --import-albert-key` accepte uniquement
une affectation littérale simple `ALBERT_API_KEY` de `.env`, sans expansion
shell. La valeur n'entre jamais dans l'aperçu ni le manifeste ; elle va dans
le magasin natif, et une entrée Albert existante différente n'est pas
remplacée. Une clé calculée ou stockée dans un profil/script doit être ajoutée
manuellement avec `just-code auth add albert`. Le fichier `.env` source reste
intact : vérifier la clé native avant toute suppression ou rotation séparée
d'une copie locale.
Le choix d'utiliser `--import-albert-key` doit être fait au premier `--apply` ;
une réimportation ne modifie pas la valeur `credentialRef` du manifeste.
L'aperçu liste les noms des variables de `.env` sans leurs valeurs, mais
n'importe aucune autre variable. Le connecteur Context7 intégré est anonyme ;
`CONTEXT7_API_KEY` reste sur l'hôte et n'est pas utilisé par ce connecteur.
Si des skills sont présents, le catalogue officiel doit être accessible pour
résoudre les révisions, y compris lors d'une réimportation.

## Modèle et configuration OpenCode

La configuration OpenCode effective est composée au lancement : l'asset
embarqué (provider Albert, permissions) fusionné avec la couche gérée
(`OPENCODE_CONFIG_CONTENT`), qui porte les champs managés : `model`,
`small_model` et la liste de modèles Albert autorisés. OpenCode fusionne le
contenu inline en dernier
([contrat D-001](decisions/2026-09-23-opencode-configuration-contract.md)) ;
la config projet et la config utilisateur survivent champ par champ en
dessous. Précédence de la sélection : `JUST_CODE_MODEL` > `model` du
manifeste projet > `defaultModel` des réglages utilisateur > valeur intégrée
(`albert/qwen3-coder`). `settings.json` conserve le modèle par défaut
(`defaultModel`) et la liste des modèles autorisés (`models`). Une référence de
modèle est `plateforme/modèle` pour Mistral AI et une plateforme compatible
OpenAI ; une valeur sans préfixe géré reste un identifiant Albert (y compris
lorsqu'il contient un `/`, comme `openai/gpt-oss-120b`). Chaque plateforme
connectée reçoit son propre bloc provider dans `OPENCODE_CONFIG_CONTENT`, avec
la variable d'environnement qui porte sa clé (`ALBERT_API_KEY`,
`MISTRAL_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`) ; un bloc Albert embarqué est
retiré quand Albert API n'est pas connectée. Un conflit entre
un champ managé et la config projet est affiché en diff avant lancement (la
valeur gérée gagne) : si la config projet définit `model` différemment, les
deux valeurs sont affichées (l'inverse serait un écrasement invisible).

Dans `just-code init`, le récapitulatif interactif présente en français les
skills, les MCP, GitHub, la machine isolée et le modèle. Il indique pour chaque
MCP distant si son initialisation MCP répond : un service injoignable affiche
un avertissement mais n'empêche pas **Appliquer**. La liste des fichiers créés
ou modifiés distingue ceux du projet de ceux conservés sur ce poste. La touche
**d** affiche le diff de la seule section just-code d'`AGENTS.md` (en mode
accessible, choisissez l'option numérotée « Afficher ou masquer le détail ») ;
le texte utilisateur hors de cette section reste intact. Les empreintes sha256
sont masquées, mais les révisions épinglées des skills restent visibles. Un
remplacement affiche aussi les valeurs de la configuration existante avant
**Appliquer**. **Modifier** rouvre les questions avec les réponses conservées,
**Réglages avancés** les réglages techniques, et **Quitter** n'écrit rien.
Le mode `init --yes` conserve la revue détaillée, notamment les révisions et
empreintes sha256 des skills.

Les réglages avancés couvrent le type de machine isolée, l'isolation, les
processeurs, la mémoire et les modèles. Ces réglages ne sont pas
demandés dans le parcours par défaut. Chaque
modification validée est conservée lorsque vous revenez au récapitulatif avec
Échap. Le modèle principal peut reprendre le défaut de `setup` ou être choisi
pour ce projet ; `JUST_CODE_MODEL` conserve sa priorité au lancement.

Le petit modèle OpenCode sert à générer les titres de session. Sans choix
explicite, il reste identique au modèle principal, y compris lorsque celui-ci
change. Les réglages avancés permettent de choisir à la place **Ministral 3
8B**, qui correspond à `ministral-3-8b-instruct-2512` sur Albert API et à
`albert/ministral-3-8b` dans la configuration OpenCode. Le choix explicite de
Ministral exige qu'Albert API soit connectée ; le suivi du modèle principal
reste disponible avec les autres plateformes. Le récapitulatif
distingue les deux modèles lorsqu'ils diffèrent.

Le choix est enregistré dans `smallModel` du manifeste projet et transmis à
`small_model` au lancement. Pour une configuration non interactive :

```bash
just-code init --root . --small-model ministral-3-8b-instruct-2512 --yes
```

Utilisez `--small-model default` pour revenir au modèle principal. La référence
d'identifiant Albert n'est plus demandée dans l'assistant ; `--credential-ref`
reste disponible.

### Harnais de l'agent

Le harnais est le logiciel d'agent exécuté dans la machine isolée. `init`
l'enregistre dans le champ `harness` du manifeste projet et le lancement le
relit ; `JUST_CODE_HARNESS` reste prioritaire pour un essai ponctuel.

- `opencode` (défaut) : l'agent historique, avec ses greffons, ses skills et ses MCP.
- `pi` : [Pi](https://github.com/badlogic/pi-mono), un agent en terminal plus léger,
  installé dans l'invité à une version épinglée par just-code.

Pi n'a pas de mode serveur : il exige l'isolation complète, où l'agent, sa
configuration et ses sessions restent dans la machine isolée. L'assistant refuse
la combinaison Pi et isolation partielle, et le lancement la refuse aussi. Sa
configuration (`models.json`, `settings.json`) est écrite dans `~/.pi/agent` de
l'invité à partir des mêmes plateformes d'inférence que celles décrites
ci-dessus, et ses skills de projet sont installés dans `~/.pi/agent/skills`.

Le script de préparation d'un invité est figé à sa création, comme son image :
changer de harnais recrée donc la machine isolée, ce qui détruit les sessions et
les fichiers propres à l'invité. Un projet qui reste sur OpenCode garde sa
révision et n'est pas recréé.

Un projet Pi écarte les modèles dont les appels d'outils en flux ne sont pas
fiables (#347). `qwen3-coder` est retiré de la liste que Pi reçoit et le repli
d'un projet Pi qui n'a choisi aucun modèle est `deepseek-v4-flash`, le premier
modèle retenu que Pi peut exécuter. Une sélection explicite de `qwen3-coder`
pour un projet Pi est refusée, d'abord à la question du modèle puis au
lancement, avec son motif : le projet enregistre un modèle qu'il peut exécuter,
ou aucun. OpenCode continue de proposer `qwen3-coder` sans changement ; les
connecteurs MCP, eux, ne s'appliquent qu'avec OpenCode, sans pont prévu pour Pi
(#346). Mesures, dates et limites :
[`docs/decisions/2026-11-09-pi-harness.md`](decisions/2026-11-09-pi-harness.md).

`just-code models` valide la sélection contre le catalogue Albert. Les modèles
`text-generation` et les modèles de chat multimodaux connus sont listés, sans
les modèles spécialisés en OCR ; le catalogue en échec réseau retombe sur le
dernier-known-good persisté dans l'état hôte. Une sélection absente du
catalogue est signalée, jamais effacée.

**Confiance locale :** les entrées de projet qui exécutent du code au chargement d'OpenCode — plugins déclarés, plugins auto-découverts (`.opencode/plugin/*.js|ts` — ils s'exécutent **sans déclaration**), commandes MCP locales, destinations MCP distantes — exigent une approbation locale avant le premier `start` du projet :

```bash
just-code trust status   # ce que le projet déclare, ce qui est approuvé, ce qui a changé
just-code trust approve  # approuve le contenu actuel de chaque entrée
```

L'approbation est liée au **contenu** (hachage par fichier) : un fichier modifié est de nouveau non approuvé, un nouveau plugin apparaissant nécessite sa propre approbation. L'enregistrement vit dans l'état hôte (`~/.local/state/just-code/projects/<projet>/opencode-trust.json`), jamais dans le dépôt — un clone n'apporte pas sa confiance avec lui. Les liens symboliques sont refusés à l'approbation (un chemin repointable n'est pas un contenu épinglé).

## MCP distants

`just-code init --mcp data-gouv` sélectionne les connecteurs distants gérés ;
`--clear-mcps` les désélectionne tous. À l'initialisation d'un nouveau projet,
`data-gouv` est présélectionné ; valider l'écran MCP sans aucune sélection
déclenche une confirmation « Continuer sans MCP ? ». Le manifeste ne contient
que leurs identifiants et la configuration générée est injectée dans OpenCode
sans modifier les fichiers du projet. Les entrées personnalisées portant un
nom différent restent intactes.

La destination gérée est `https://mcp.data.gouv.fr/mcp` (service public, appels
anonymes ; `init` n'exige ni ne transmet de clé).

Les connecteurs retirés du catalogue (`context7`) restent tolérés dans un
manifeste existant mais y restent inactifs : l'assistant `init` les affiche
comme non repris au récapitulatif, `mcp status` les liste sans les sonder, et
l'import depuis Albert Code les signale comme non repris au lieu de les
importer. Une nouvelle sélection (`--mcp` ou l'écran interactif) les refuse.

`just-code mcp status` envoie un MCP `initialize` depuis l'hôte et distingue la
configuration, la vérification du protocole, l'authentification requise, un
accès refusé, une indisponibilité réseau et une dérive de contrat. Le contrôle
est anonyme et ne qualifie donc jamais une réponse 403 d'identifiants invalides.
Il ne prouve pas la connectivité du guest ni l'exécution d'un appel d'outil
depuis OpenCode.

## MCP navigateur

`chrome-devtools` est un processus local au guest, distinct des destinations
distantes :

```bash
just-code init --root . --mcp chrome-devtools --yes
```

La sélection compose cet outil dans OpenCode sans démarrer de navigateur sur
l'hôte. Chrome DevTools se connecte au serveur de débogage lié à
`127.0.0.1:9222` dans ce même guest. Le statut MCP sur l'hôte ne sonde pas ce
processus.

Le profil navigateur est séparé du guest léger : Debian bookworm-slim est
épinglé par digest, avec Chromium `154.0.8037.92` (amd64) / `154.0.8037.57`
(arm64), `fonts-liberation` `1:1.07.4-11`, Node.js
`22.14.0` et Chrome DevTools MCP `1.10.1`. Le
provisionnement vérifie les empreintes et marqueurs avant de déclarer le profil
prêt. Les paquets navigateur sont téléchargés lors du premier démarrage du
profil ; l'initialisation n'installe ni ne redémarre rien dans un guest en
cours. Un changement de profil d'image demande une recréation explicite, qui
efface l'état invité : exporter d'abord les éventuels changements non exportés.

`playwright` a été retiré du catalogue : présent dans un manifeste existant, il
reste inactif (non repris) et n'est plus proposé ni installé.

L'accès au serveur local de développement est possible seulement si ce serveur
est joignable depuis le guest. Aucun tunnel automatique vers `localhost` de
l'hôte n'est fourni. La qualification réelle du profil doit être exécutée sur
macOS arm64 et x86_64 ; le passage sur le runtime hôte Linux/KVM reste également
à qualifier.

## `just-code config`

- `just-code config explain` : affiche chaque champ géré avec sa valeur
  effective et sa source. Lecture seule ; n'affiche jamais de valeur secrète.
- `just-code config import-env <chemin>` : prévisualise l'import borné d'un
  `.env` legacy. Les clés reconnues sont mappées, les clés inconnues listées,
  les clés d'identifiants (dont `ALBERT_API_KEY`) ne sont jamais copiées :
  l'outil signale qu'elles doivent rejoindre le magasin d'identifiants. Le
  fichier original n'est jamais modifié.

## Transition legacy

Le fichier `.env` n'est plus lu du tout : `LoadConfigEnv`
résout la configuration depuis l'environnement du processus, et un `.env`
présent est signalé avec la commande qui l'adopte
(`just-code config import-env <chemin>`, qui prévisualise et ne modifie jamais
l'original). Les variables non préfixées restent lues depuis l'environnement
pour l'instant ; leur retrait est prévu dans la première version mineure qui
suit l'achèvement du réglage par défaut entièrement typé.

## Compatibilité

`LoadConfig` garde le comportement « déjà exporté gagne », mais **ses défauts
ont changé** : `RUNTIME` vaut `microsandbox` (au lieu d'aucun défaut), et
`ISOLATION` vaut `full` (au lieu de `backend`). `WORKSPACE_DIR` n'a plus de
valeur par défaut dans le fichier : le lancement utilise la racine du projet
découverte quand rien n'est configuré, et un `WORKSPACE_DIR` explicite est
honoré tel quel (`Config.WorkspaceDirSet` enregistre cette provenance).
