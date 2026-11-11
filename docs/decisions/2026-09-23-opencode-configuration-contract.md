# D-001 : contrat de configuration et de découverte d'OpenCode

Date : 23 septembre 2026
Statut : accepté
PR : P01 (`test: caractériser la configuration effective d'OpenCode`)
Suivi : #72 (preuves de compatibilité), #29 (contrat de configuration)

## Contexte

codebox fournit aujourd'hui la configuration OpenCode par deux mécanismes :
l'asset embarqué `assets/opencode-config.json` injecté comme variable
d'environnement invité `OPENCODE_CONFIG_CONTENT`, et la config projet
éventuellement présente dans le checkout monté. P10 doit composer une
configuration effective sans casser les réglages utilisateur, mais le
comportement réel d'OpenCode vis-à-vis de la fusion de ces sources n'était pas
mesuré : la documentation d'OpenCode décrit des mécanismes, mais le plan P01
exige une vérification empirique sur un processus OpenCode épinglé, pas une
lecture de docs.

## Méthode

OpenCode `opencode-ai@1.18.32` (version npm épinglée, la même famille de
binaires que l'image invité `ghcr.io/anomalyco/opencode`) installé hors VM sur
Linux x86_64, piloté via les commandes réelles `opencode debug config`,
`opencode debug skill`, `opencode debug paths` et `opencode run`. Les appels
`opencode run` ont été exécutés avec un fournisseur Albert réel (clé Bearer
via `{env:ALBERT_API_KEY}`) pour vérifier la sélection effective de modèle.
Chaque constat ci-dessous est une observation, pas une inférence. Le harnais
`tests/integration/opencode/` rejoue les cas non réseau de cette matrice.

## Constats

### Fusion des sources et précédence

Ordre de fusion observé (du plus faible au plus fort) :

1. config globale (`$XDG_CONFIG_HOME/opencode/opencode.json[c]`, sinon
   `~/.config/opencode/opencode.json[c]`) ;
2. config projet découverte en remontant du répertoire courant vers la racine
   du worktree Git (`opencode.json`, `opencode.jsonc`,
   `.opencode/opencode.json`, `.opencode/opencode.jsonc`) ;
3. config explicite `OPENCODE_CONFIG=/chemin` — chargée **en plus**, dans
   l'ordre de découverte normal, pas au-dessus de la config projet ;
4. contenu inline `OPENCODE_CONFIG_CONTENT` — fusionné **en dernier**, portée
   locale : c'est la source la plus forte.

La fusion est un deep-merge champ par champ : la source gagnante écrase les
champs qu'elle définit, les autres champs des sources plus faibles survivent
(vérifié : `permission.bash` globale survit à un `model` posé par
`OPENCODE_CONFIG_CONTENT` ; `small_model` globale survit à un `model` projet).

Conséquence directe pour codebox : **`OPENCODE_CONFIG_CONTENT` est un
mécanisme de fusion finale officiel d'OpenCode, pas un hack** — le mécanisme
actuel de codebox est valide, mais il écrase tout champ qu'il définit, donc
le compositeur de P10 ne doit y mettre que les champs gérés, et doit rendre
les conflits détectables avant d'écraser un réglage utilisateur.

### `OPENCODE_CONFIG` n'est PAS une surcharge

`OPENCODE_CONFIG` charge un fichier supplémentaire qui participe à la fusion
au même rang que les autres fichiers de config ; testé en conflit direct avec
la config projet, la config projet gagne. Une surcharge « au-dessus du
projet » n'existe pas par cette variable. P10 ne doit pas l'utiliser pour
imposer des champs gérés contre la config projet : le contenu inline
(`OPENCODE_CONFIG_CONTENT`) est le seul chemin qui gagne contre le projet.

### Échappatoires vérifiées

- `OPENCODE_DISABLE_PROJECT_CONFIG=1` ignore toute la config projet (utile
  pour le diagnostic d'une config projet cassée).
- `OPENCODE_PURE=1` / `--pure` désactive les plugins externes (voir trust
  contract ci-dessous).
- `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` ne désactive PAS les plugins
  auto-découverts du projet.
- `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` coupe les scans `~/.claude/skills` et
  `~/.agents/skills`.

### JSONC

`opencode.jsonc` et `.opencode/opencode.jsonc` sont découverts et parsés
(commentaires acceptés). OpenCode ne réécrit jamais le fichier source : le
commentaire de test a survécu à tous les chargements. Le compositeur de P10
ne doit jamais réécrire un fichier JSONC utilisateur ; s'il doit modifier la
config projet, il passe par la fusion inline ou par un fichier géré distinct.

### Découverte de skills et d'instructions

- Skills projet : `.opencode/skills/<nom>/SKILL.md` (et alias
  `.opencode/skill/`), découverts depuis la racine projet ; skills globaux :
  `$XDG_CONFIG_HOME/opencode/skills/` ; skills externes auto-chargés depuis
  `~/.claude/skills/` et `~/.agents/skills/`. `skills.paths` (objet
  `{"paths": [...]}`) permet d'enregistrer des répertoires supplémentaires.
- `AGENTS.md` à la racine projet est chargé automatiquement comme
  instructions système, même sans champ `instructions` ; le champ
  `instructions` permet d'en ajouter d'autres. Une entrée `instructions`
  pointant un fichier absent est silencieusement ignorée.
- Un skill sans `description` en frontmatter est filtré et jamais exposé au
  modèle.

### Clés inconnues

Une clé de premier niveau inconnue dans un fichier de config est ignorée
silencieusement dans la sortie fusionnée (testé : `totally_unknown_key_xyz`
absent du `debug config`). La validation stricte documentée ne s'observe pas
sur ce chemin en 1.18.32 : ne pas compter sur un rejet pour détecter les
champs gérés en conflit ; la détection doit être faite par le compositeur
codebox lui-même.

### Exécution de code (contrat de confiance)

- Un plugin déclaré dans `plugin` (config projet ou globale) **exécute du
  code arbitraire au chargement de la config** — prouvé par effet de bord
  (écriture de fichier via `child_process`).
- Un plugin **auto-découvert** dans `.opencode/plugin/` ou
  `.opencode/plugins/` (`.js`/`.ts`, aucune déclaration) **exécute aussi du
  code arbitraire au chargement** — même preuve par effet de bord. Le variant
  `.mjs` n'est PAS auto-découvert (observed : aucun effet).
- `OPENCODE_PURE=1` / `--pure` empêche l'exécution des plugins auto-découverts
  et déclarés. `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` ne les empêche pas.

**Conséquence pour le contrat de confiance de P10 :** un dépôt cloné peut
exécuter du code au démarrage d'OpenCode sans aucune déclaration, via
`.opencode/plugin/*.js`. Le modèle scellé (P22) limite ce que ce code peut
atteindre (pas de checkout hôte monté, identifiants liés à des destinations),
mais P10 doit traiter la présence de fichiers de plugin projet comme un
signal d'exécution nécessitant approbation locale, au même titre que les
commandes MCP gérées. L'option `--pure` existe comme levier de diagnostic
mais ne peut pas être le mode par défaut sans casser les plugins légitimes
des utilisateurs.

### Sélection effective de modèle (bout en bout)

Avec le provider Albert de `assets/opencode-config.json` et une vraie clé
Bearer : `opencode run` résout `albert/deepseek-v4-flash`, appelle l'API
réelle, charge `AGENTS.md`, découvre les skills projet et répond. La
résolution effective est observable dans les logs de session
(`model=albert/deepseek-v4-flash`) — c'est le chemin de vérification que P10
utilisera après création/redémarrage de l'invité.

## Décisions pour P10

1. **Overlay invité = contenu inline.** codebox compose sa couche gérée
   dans `OPENCODE_CONFIG_CONTENT` (mécanisme de fusion finale officiel). La
   config projet et la config globale de l'utilisateur fusionnent en dessous
   et survivent champ par champ.
2. **Détection de conflit côté codebox.** OpenCode n'expose pas de diff de
   fusion ; avant d'écraser un champ géré, le compositeur lit la config
   projet (JSONC parsé en lecture seule) et signale les conflits champ par
   champ à l'utilisateur.
3. **Jamais de réécriture de JSONC utilisateur.** Les fichiers projet
   restent intouchés ; la couche gérée vit dans l'overlay inline, les
   fichiers gérés distincts (skills, agents) vivent dans des chemins que
   codebox possède.
4. **Plugins projet = signal d'approbation.** La présence de
   `.opencode/plugin/*.js|ts` ou d'une entrée `plugin` dans la config projet
   est signalée dans la revue de confiance de P10 comme exécution de code
   potentielle, avant le premier démarrage sur ce projet.
5. **Pas de parser JSONC maintenu à importer.** Les besoins de codebox
   sont couverts par : lecture (tolérante aux commentaires, harnais de test
   embarqué) et écriture uniquement de fichiers que codebox possède au
   format JSON strict. Aucun import de dépendance JSONC dans ce PR.

## Limites

- Mesures faites hors microVM sur Linux x86_64 ; la version invité
  (`ghcr.io/anomalyco/opencode`) peut différer de 1.18.32. P03 doit
  confirmer la version réelle de l'image invité et rejouer le cas
  d'exécution de plugin dans l'invité.
- Les appels `opencode run` réels ont utilisé le réseau ; le harnais de test
  embarqué couvre les cas non réseau (fusion, découverte, JSONC,
  auto-découverte de plugins) et marque les cas réseau comme opt-in.

## Addendum P10 (2026-09-24) : implémentation

P10 applique les décisions de ce document :

- **Composition.** `ComposeConfigContent` fusionne côté hôte l'asset embarqué
  et la couche managée (`model`, `small_model` uniquement) en un seul
  document JSON — pas de dépendance envers l'analyse de documents JSON
  multiples par OpenCode. La fusion finale d'OpenCode (contenu inline en
  dernier) reste le mécanisme : la config projet et utilisateur survivent
  champ par champ en dessous. La sélection suit `CODEBOX_MODEL` >
  manifeste > réglages utilisateur > défaut intégré ; le contenu composé est
  rafraîchi au prochain démarrage d'une instance arrêtée comme à la création.
- **Conflits.** `DetectOverlayConflicts` compare la couche managée à la
  config projet (JSONC lu en lecture seule, harnais interne sans
  dépendance) et affiche un diff champ par champ avant lancement ; la valeur
  gérée gagne, mais jamais silencieusement.
- **Confiance.** `trust status|approve` enregistre l'approbation locale par
  **contenu haché** (plugins déclarés et auto-découverts, commandes MCP
  locales ; la config déclarante est approuvée par son propre hachage). Un
  fichier modifié est de nouveau non approuvé ; les liens symboliques sont
  refusés (un chemin repointable n'est pas un contenu épinglé) ; l'enregistrement
  ne quitte jamais l'état hôte. `start`/`attach`/`restart`/`recreate`
  refusent tant qu'une entrée est non approuvée — les commandes en lecture
  seule (`stop`, `check`, `logs`, `shell`) ne sont pas bloquées.
- **Catalogue.** `codebox models` valide contre
  `https://albert.api.etalab.gouv.fr/v1/models` (les `text-generation` et les
  modèles de chat multimodaux connus sont listables, sans les modèles OCR) ; un échec réseau retombe sur le dernier-known-good
  (`catalogue.json` en état hôte). Les limites viennent de l'entrée du
  catalogue uniquement — un identifiant absent n'a pas de limites
  fabriquées — et une sélection introuvable est signalée, jamais effacée.
