# D-003 : capacités invité Microsandbox et comportement PTY (P03)

Date : 24 septembre 2026
Statut : accepté
PR : P03 (`test: qualifier les capacités invité et le PTY Microsandbox`)
Suivi : #72 (preuves de compatibilité), #73 (instances/cycle de vie), #29

## Contexte

Le modèle scellé P22 suppose un invité capable d'héberger OpenCode (avec
Node/npm), `gh`, un navigateur réel (Chromium), les serveurs MCP navigateur
(Playwright MCP, Chrome DevTools MCP), et des opérations Git invité (clone
depuis l'origine ou depuis un chemin hôte, sous-modules, worktrees liés).
P01 avait caractérisé la configuration OpenCode hors VM ; P02 le transport
des identifiants. P03 devait choisir une fondation invité pratique et
vérifier l'expérience du mode `full` : PTY (redimensionnement, Ctrl+C,
sortie propre, reconnexion, propriété du handle SDK), cycle de vie, et les
mesures de clone invité qui conditionnent P22 et donc M1.

## Méthode

Runtime Microsandbox CLI **0.7.0** (macOS, libkrun, Apple Silicon), SDK Go
**v0.7.2**. Deux images invité comparées : `alpine` (4.0 MiB) et
`debian:bookworm-slim` (26.8 MiB), les deux en arm64. Chaque constat est
une observation (commandes réelles `msb create/exec/run/ssh`, outils
installés dans l'invité, navigation/capture/MCP réels), pas une inférence.
Le harnais `tests/integration/guest-capabilities/` rejoue les cas T1-T10 ;
les mesures sont des chiffres de session sur machine de développement, pas
des garanties de performance.

## Constats

### Images et empreinte de base

- **Taille d'image** : alpine 4.0 MiB, debian:bookworm-slim 26.8 MiB,
  `ghcr.io/anomalyco/opencode:latest` 71.0 MiB (image actuelle).
- **Création à froid** (image déjà en cache) : alpine ~210 ms, debian
  ~220 ms — pas de différence significative.
- **RAM au repos** : ~56 MiB / 512 MiB alloués sur les deux invités.
- **Disque invité provisionné** (Debian, outillage complet : curl, git,
  Node 22.14, OpenCode 1.18.32, Chromium système, Playwright MCP) :
  ~2.6 Go utilisés sur 3.9 Go, dont Chromium 350 Mo, node_modules
  456 Mo (opencode + playwright-mcp + chrome-devtools-mcp), cache
  navigateur Playwright 662 Mo (chrome-for-testing, inutilisé — voir
  limites). L'image alpine reste à ~291 Mo pour Chromium et ~12 Mo pour
  npm-global.
- **Architecture** : les deux images testées sont arm64 ; la colonne
  x86_64 reste à mesurer (non rejoué, machine arm64).

### Chaîne d'outils invité

- **Debian** : `apt` complet. curl 7.88, git 2.39, ca-certificates,
  xz-utils installables ; Node 22.14.0 + npm 10.9.2 via tarball officiel
  (le paquet `nodejs` Debian n'a pas été testé) ; OpenCode 1.18.32 via
  `npm install -g opencode-ai@1.18.32` ; `gh` présent mais **ancien**
  (2.23.0, bookworm/main) — insuffisant pour les fonctions récentes, à
  remplacer par la release officielle GitHub (binaire) si Debian est
  choisi.
- **Alpine** : `apk` rapide. nodejs 24.18.1 + npm 11.12.1, git 2.54.0,
  chromium 152.0.7977, curl installables. Pas de `gh` dans les dépôts
  testés (non vérifié exhaustivement — edge/community peuvent le porter).

### Navigateur et MCP navigateur

- **Chromium headless réel** (les deux invités) : `--dump-dom` sur
  `example.com` renvoie le `<h1>` ; capture d'écran PNG non vide
  (17 Ko, relue visuellement : rendu réel de `Example Domain`).
- **Playwright MCP** (1.64.0-alpha) : avec le **chromium système** via
  `--executable-path`, `browser_navigate` + `browser_snapshot` sur
  `example.com` réussissent dans les deux invités. **Sans** ce flag, le
  MCP attend son propre `chrome-for-testing` (1246) et échoue avec
  `Browser "chrome-for-testing" is not installed` — le navigateur
  auto-téléchargé n'est pas un repli fiable.
- **Chrome DevTools MCP** (1.10.1) : se connecte à un chromium système
  lancé avec `--remote-debugging-port` via `--browserUrl`; `list_pages`,
  `navigate_page`, `take_snapshot` réussissent. La navigation exige un
  `pageId` numérique (les appels sans `pageId` échouent en validation).
- Les deux serveurs MCP parlent le protocole MCP stdio réel
  (initialize / notifications/initialized / tools/call), pas un
  sous-ensemble.

### PTY et cycle de vie

- **`msb run --tty`** ouvre un PTY dont la taille suit le terminal hôte
  (observé `24 80` hors pty, `40 120` sous un pty `script` configuré en
  120x40). C'est le chemin qui portera le TUI OpenCode invité.
- **`msb exec` est non-TTY par défaut** (`stty size` → `Inappropriate
  ioctl`). `msb ssh` **non interactif** est non-TTY aussi. Un `msb ssh`
  interactif sous un pty hôte ouvre bien un shell invité.
- **Ctrl+C / SIGINT** : un `SIGINT` envoyé au processus client `msb run`
  **ne tue pas la commande invité** (un `sleep 60` invité a survécu à un
  SIGINT client). Le comportement attendu d'un TUI (SIGINT transmis à
  l'invité) n'est pas observé par ce chemin ; à confirmer dans un vrai
  terminal interactif avant de figer le contrat PTY de P22.
- **Reconnexion** : un `msb exec` pendant qu'un autre exec tourne
  fonctionne ; `stop`/`start` du sandbox est propre et l'état invité
  (fichiers, clone Git) **persiste** après redémarrage.
- **Propriété du handle SDK** : `Detach` libère la propriété sans arrêter
  la VM (déjà couvert par `TestDetachMSBSandbox` ; la session a confirmé
  le comportement CLI équivalent).

### Clone invité (entrée de conception P22)

- **Origine distante** : `git clone --depth 1` de `etalab-ia/codebox`
  (public) → **853 ms**, `.git` = 360 Ko, arbre de travail 1.1 Mo, HEAD
  = `2cf7adc`. codebox n'a pas de sous-modules (`git submodule status`
  vide, aucune entrée `160000` dans l'index).
- **Chemin local** (proxy du clone depuis l'hôte) : clone depuis un clone
  invité en **29 ms**, mais **l'origine est le chemin source**
  (`/tmp/cb-remote`), pas l'URL distante — P22 doit réécrire
  `remote.origin.url` après clone, ou toujours cloner depuis l'origine
  distante.
- **Worktree lié invité** : `git worktree add` dans un clone invité
  fonctionne (le `.git` du worktree lié est un gitfile pointant vers
  `<clone>/.git/worktrees/<nom>`) ; `status` propre. Compatible avec le
  workflow Letta Code (worktrees `.letta/worktrees/`) à l'intérieur d'un
  clone possédé par l'invité.
- **Sous-modules** : non rejoués (codebox n'en a pas) ; un dépôt avec
  sous-modules exigerait `git submodule update --init --recursive` après
  le clone scellé, ce que P22 devra tester sur un dépôt réel en ayant.

## Décisions

1. **Fondation invité : Debian épinglé pour le mode navigateur, Alpine
   reste viable en repli léger.** Debian bookworm-slim apporte `apt`
   complet et un Chromium système 153 ; Alpine est 6.7x plus petit et
   tout aussi rapide à froid, avec une chaîne npm plus fraîche. Le choix
   n'est **pas** un switch d'image de production : c'est le candidat pour
   P16, conditionné aux mesures navigateur (gate P03 → P16). Un pipeline
   d'image maintenu est hors périmètre (gate du plan).
2. **Chromium système + `--executable-path` pour les MCP navigateur.**
   Ne pas compter sur le téléchargement Playwright dans l'invité : il
   n'est pas rejoué ici et alourdit l'empreinte (662 Mo) sans être
   utilisé. P16 doit provisionner un chromium système et passer le chemin
   d'exécutable.
3. **P22 doit cloner depuis l'origine distante par défaut.** Le clone
   local est 30x plus rapide mais laisse une origine incorrecte ; le
   chemin sûr est clone distant + profondeur bornée, avec refus explicite
   des agencements non supportés (modèle scellé sans montage hôte).
4. **Le contrat PTY de P22 doit être vérifié en terminal interactif
   avant M1.** La propagation de taille est prouvée ; la transmission de
   SIGINT vers l'invité ne l'est pas par le chemin `msb run` client. C'est
   le dernier trou de la qualification PTY (la session actuelle ne
   fournit pas de vrai terminal interactif).

## Limites

- Mesures sur macOS arm64 (libkrun) uniquement ; x86_64 et le runtime hôte
  Linux/KVM restent à caractériser.
- Le Ctrl+C/SIGINT n'est pas tranché : la non-transmission observée peut
  être un artefact de la livraison d'un signal au processus client hors
  vrai terminal, pas le comportement TUI réel. À reprendre en session
  interactive.
- Le navigateur auto-téléchargé de Playwright n'a pas été rejoué après le
  flag `--executable-path` ; `install-browser chrome-for-testing` existe
  mais n'a pas été mesuré.
- `gh` invité : version Debian trop ancienne (2.23.0) ; ni la release
  officielle ni l'état Alpine des dépôts n'ont été caractérisés.
- Le comportement des sous-modules et des dépôts de taille réaliste
  (centaines de Mo) est non rejoué — P22 devra le mesurer sur un dépôt
  réel en ayant avant de geler le contrat de clone.

## Requalification v0.7.3 (2026-10-02)

Suite à la montée du runtime Microsandbox de v0.7.2 vers v0.7.3 (issue
#104, PR #107), la matrice d'intégration P03 a été rejouée sur Mac
Apple Silicon avec les cas réseau activés.

**Résultats :** 16/16 passent (T1–T10c), incluant :

- Images invité (alpine, debian:bookworm-slim)
- Démarrage à froid (< 260ms)
- Persistance d'état après redémarrage
- Propagation PTY (40x120)
- Node.js v22.14.0 invité
- OpenCode 1.18.32 invité
- Chromium headless + capture d'écran
- Playwright MCP (chromium système via `--executable-path`)
- Clone depuis l'origine distante (< 1s, profondeur 1)
- Clone host-path + worktree lié

L'assertion T7a a été mise à jour : example.com a été redesigné et ne
contient plus de balise `<h1>` ; le harnais cherche désormais le titre
« Example Domain ».

**Conclusion :** Aucune régression des capacités invité dans
Microsandbox v0.7.3. Le runtime passe la matrice complète.

## Requalification v0.7.6 (#235, 2026-10-27)

Le harnais P03 est aligné sur le runtime CLI 0.7.6 et le SDK Go v0.7.6 livrés
par codebox. La matrice CLI a été rejouée sur MacBook-Pro-Luis.local, sous
macOS 27.0.1 (arm64), avec libkrun. Elle n'exécute pas le SDK Go : la sortie
`go list -m` conserve sa version déclarée dans la révision `7f8b3de`. Le
runtime temporaire a été vérifié par le SHA-256 gravé dans codebox. Le
`~/.microsandbox` du poste n'a pas été modifié.

**Résultats observés :**

- Sans les cas réseau : 8 réussites, 0 échec. La version du CLI, les images,
  le cycle de vie, la persistance et le PTY passent. Le script signale
  explicitement que les cas réseau sont ignorés.
- Avec `MSB_GUEST_INTEGRATION_NETWORK=1` : 18 réussites, 0 échec. T1-T10c
  passent, notamment Node v22.14.0, OpenCode 1.18.32, Chromium, Playwright
  MCP, le clone distant et le worktree lié invité.
- Le clone distant a pris 1,253 seconde. Le clone depuis le chemin hôte monté
  a pris 106 ms. Ce sont des mesures de session, pas des objectifs de
  performance.

T1 vérifie désormais les références Alpine et Debian et leur architecture
arm64. Ces trois assertions remplacent l'ancienne réussite inconditionnelle
relevée en revue. Leur taille reste hors assertion.

Le prévol T10 refuse une archive sans `.git` avec un diagnostic explicite et
la sortie 2, avant d'arrêter ou de retirer sa sandbox. Le run réseau complet
utilise un clone principal temporaire. Un worktree lié reste possible lorsque
son clone principal est accessible. Les trois transcripts de `7f8b3de` sont
conservés dans `tests/integration/guest-capabilities/evidence/`, avec la
commande complète, les versions et la sortie brute finale de `msb list`.

Cette qualification concerne les capacités invité P03. Elle ne requalifie ni
le transport des identifiants P02, ni les parcours intégrés à codebox. Le
profil navigateur P16 et les autres limites déjà documentées restent hors de
cette matrice.

## Requalification v0.7.8 (#235, 2026-10-27)

`main` a livré MSB 0.7.8 pendant cette requalification (PR #283). La matrice a
donc été rejouée sur cette version, qui est celle que le harnais doit suivre.
Les résultats 0.7.6 ci-dessus restent dans l'historique comme mesures de la
version livrée à l'ouverture de l'issue.

**Résultats observés sur macOS 27.0.1 arm64, libkrun :**

- Sans les cas réseau : 8 réussites, 0 échec, avec un `MSB_HOME` vidé au
  préalable. T1 télécharge les images absentes avant de les inspecter.
- Avec `MSB_GUEST_INTEGRATION_NETWORK=1` : 18 réussites, 0 échec, T1-T10c.
- Sans checkout Git : sortie 2 au prévol T10, avant l'arrêt de la sandbox.
- `msb list` final vide après chacun des trois runs.

Le runtime temporaire a été vérifié par le SHA-256 gravé dans codebox
(`5dbe0d92…`) et `msb doctor` confirme libkrunfw et Apple Silicon. Les
transcripts de `e1484a7` sont dans
`tests/integration/guest-capabilities/evidence/`.

Une correction du harnais a été nécessaire : sur un `MSB_HOME` neuf, l'ancien
T1 échouait en `image not found`. `ensure_image` télécharge désormais l'image
absente, ce que le transcript `darwin-arm64-msb-0.7.8-t1-precondition.txt`
documente. Cette qualification 0.7.8 reste une matrice CLI P03 : elle ne
requalifie ni P02, ni les parcours intégrés, ni le profil navigateur P16.

## État d'implémentation navigateur P16 (2026-10-04, qualification en attente)

Le changement P16 ajoute un profil navigateur distinct, sans modifier l'image
légère par défaut. Le profil utilise Debian bookworm-slim, index multi-architecture
épinglé par digest `sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251`,
le snapshot APT `20261002T000000Z`, Chromium `154.0.8037.92` (amd64) /
`154.0.8037.57` (arm64), `fonts-liberation` `1:1.07.4-11` et Node.js
`22.14.0` vérifié par SHA-256 sur
amd64 et arm64. OpenCode reste à `1.18.32`; les MCP locaux sont Playwright
`0.0.82` et Chrome DevTools `1.10.1`.

Les MCP navigateur ne sont pas qualifiés par le résultat P03 v0.7.3 ci-dessus :
ce résultat vérifie le runtime et le MCP Playwright de la matrice, pas le nouveau
profil épinglé, le cycle d'idempotence de son provisionnement, ni son overlay
OpenCode dans les deux architectures. Ils ne sont pas encore exécutés dans un
guest réel au moment de cette note.

**Qualification P16 à réaliser avant de revendiquer la parité navigateur :**

- Rejouer le démarrage, la persistance et le provisionnement sur macOS arm64 et
  x86_64 ; vérifier `node`, Chromium, les versions des deux MCP et les marqueurs
  de profil dans le guest.
- Ajouter les deux MCP puis chacun séparément ; confirmer que `tools/list` de
  l'OpenCode invité les expose, et qu'un second démarrage avec la même empreinte
  ne retélécharge ni ne réinstalle les paquets.
- Exercer Playwright sur un serveur de développement démarré dans le guest ;
  confirmer qu'aucun tunnel ou accès au `localhost` de l'hôte n'est présumé.
- Vérifier que le listener DevTools est lié à `127.0.0.1:9222` dans le guest,
  qu'il n'est pas joignable depuis l'hôte, et qu'un changement de profil refuse
  de reprendre un guest existant sans recréation explicite.
- Rejouer la matrice de ressources sur les hôtes macOS, Windows et Linux : les
  valeurs détectées doivent rester des recommandations, jamais une réduction
  silencieuse des demandes de l'utilisateur.

La couverture Go et la syntaxe du script de préparation ne remplacent pas ces
tests d'intégration. Le runtime hôte Linux/KVM reste une qualification séparée.

## Épinglage de l'image Microsandbox par défaut (2026-10-05)

P18 remplace la référence flottante `ghcr.io/anomalyco/opencode:latest` par
l'index OCI multi-architecture immuable
`ghcr.io/anomalyco/opencode@sha256:b34342987ca889fc2cc19cbc046eefc2418e5980a3d696e209fbb401a288f631`.
Le digest a été lu dans `Docker-Content-Digest` sur le registre GHCR pour le
manifest accepté OCI. Une mise à niveau depuis codebox 0.7.0 peut donc
classer un invité existant comme nécessitant une recréation ; celle-ci reste
explicite et détruit les sessions, outils et fichiers propres à l'invité.
