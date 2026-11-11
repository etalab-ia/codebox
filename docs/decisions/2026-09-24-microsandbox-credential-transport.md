# D-002 : transport des identifiants Microsandbox (substitution proxy)

Date : 24 septembre 2026
Statut : accepté
PR : P02 (`test: caractériser le transport des identifiants Microsandbox`)
Suivi : #74 (chantier identifiants), #72 (preuves de compatibilité), #29 (contrat de configuration)

## Contexte

Le plan de parité Albert Code suppose que le mode `full` de codebox peut
provisionner des identifiants (Albert Bearer, GitHub `gh`/Git HTTPS,
Context7) dans un invité Microsandbox sans exposer la valeur brute à
l'invité. Le mécanisme supposé est le proxy réseau du runtime : l'invité ne
voit qu'un placeholder `$MSB_<VAR>`, et le proxy substitue la valeur réelle
dans le transport vers les destinations autorisées. P01 avait caractérisé la
configuration OpenCode ; P02 devait établir empiriquement ce que ce
mécanisme protège, persiste et permet de mettre à jour — et s'arrêter sur un
choix de conception si Basic auth ou Context7 ne pouvaient pas être
protégés.

## Méthode

Runtime Microsandbox CLI **0.7.0** (binaire hôte macOS, libkrun) et SDK Go
**v0.7.2** (celui de `go.mod`), image invité `alpine`, sur macOS
(Apple Silicon). Laboratoire local : serveur écho HTTPS (`p02lab.test`,
port 8443), second hôte `evil.test` résolu par un serveur DNS dédié, CA
d'interception TLS fournie au runtime (`--tls-intercept*`,
`--tls-upstream-ca-cert`), valeur canari jamais publiée. Appels réels :
écriture Git HTTPS sur un dépôt privé GitHub **jetable**
(`kaaloo/p02-git-write-test`, archivé après la session ; jamais le dépôt de
production). Chaque constat ci-dessous est une observation, pas une
inférence. Le harnais `tests/integration/credentials/` rejoue les cas
rejouables (T1-T11) ; les cas exigeant des identifiants réels sont décrits
ici sans être automatisés.

## Constats

### Substitution et canaux

- L'invité ne voit **que** le placeholder (`$MSB_P02_CANARY`), jamais la
  valeur. Aucun processus invité, aucun fichier invité, aucun journal invité
  ne contient la valeur brute (scan `/proc/*/environ`, `/tmp`, `/root`,
  `/home`, `/var`, logs).
- La substitution par défaut couvre **les en-têtes uniquement**
  (`substitution: {headers: true, query: false, body: false}`). Un
  placeholder dans la query est une **violation** même vers l'hôte autorisé
  (`location=query` dans le journal runtime) : la requête est bloquée.
- **Basic auth est substitué** : git encode `user:pass` en base64 dans
  l'invité ; le proxy reconnaît le placeholder **base64-encodé** et
  substitue la valeur réelle (le serveur a reçu
  `Basic base64(user:<valeur-réelle>)`, aucune violation journalisée).
- **Preuve Git HTTPS réelle** : clone **et push** réussis depuis l'invité
  vers un dépôt privé GitHub jetable, avec un credential helper qui ne
  retourne que le placeholder. La valeur brute est absente de la config git
  invité, du helper, du dépôt et de l'hôte.
- Context7 (serveur MCP distant) s'authentifie par en-tête
  `Authorization: Bearer` (ou `X-API-Key` etc.) : même canal que le cas
  Bearer prouvé ci-dessus. Conclusion tirée de la source du serveur
  (`upstash/context7`, `extractApiKey`) et du cas d'en-tête empirique —
  non rejouée bout en bout avec une clé Context7 réelle.

### Destinations et violations

- Placeholder vers un hôte **non autorisé** : `block-and-log` — la requête
  est réinitialisée, jamais livrée, et le journal runtime enregistre
  `secret violation: placeholder detected for disallowed host` avec
  `sni`, `host`, `location`, `match_form`.
- **Redirection** : hôte autorisé 302 -> hôte non autorisé ; le premier
  saut est substitué, le second est bloqué (violation `location=header`,
  `sni=evil.test`). Le placeholder ne suit pas une redirection sortante de
  la liste d'autorisation.
- L'autorisation exige que **le moteur** résolve le nom : une entrée
  `/etc/hosts` invite ne suffit pas (violation malgré un SNI correspondant).
  Le serveur DNS dédié du laboratoire était nécessaire.
- `require_tls_identity: true` par défaut : pas de substitution sans
  identité TLS vérifiée. Le HTTP non-TLS n'a pas été caractérisé (hors
  modèle, serveur de labo TLS-only) — non testé, pas « sûr ».

### Persistance hôte (écart majeur entre chemins d'entrée)

- **Chemin CLI** (`msb create --secret 'ENV@HOST'`) : la base hôte
  (`~/.microsandbox/db/msb.db`) persiste uniquement
  `{"kind": "env", "var": "ENV"}` — **aucune valeur brute**. `msb inspect`
  ne fuit pas la valeur.
- **Chemin SDK Value** (`msb.Secret.Env(var, value, ...)`,
  `SecretModifySpec{Value: ...}`) : la même base persiste la **valeur brute
  en clair** dans le JSON de config (constaté sur la sandbox de production
  existante `albert-opencode-sandbox` : `secrets[].value` contient la clé
  Albert réelle). La base est en mode **0644** (lisible par tout utilisateur
  local) et `msb inspect --format json` imprime la valeur.
- Conséquence : le contrat « la valeur ne franchit jamais la FFI vers
  l'invité » (doc SDK) est respecté côté invité, mais le chemin SDK
  **persiste la valeur côté hôte**. codebox utilise aujourd'hui le chemin
  SDK Value (`msbCreateOptions`/`msbNextStartOptions` passent
  `spec.APIKey`/`apiKey` en valeur) : c'est l'écart à corriger en P09.

### Cycle de vie (rotation, ajout, suppression)

- **Rotation d'un secret existant** sur un invité **en cours d'exécution** :
  appliquée **immédiatement**, sans redémarrage (`msb modify --secret`).
- **Ré-ajout après suppression** : refusé sans `--next-start` ; la
  modification `next_start` exige que la variable d'environnement hôte
  existe **au moment du démarrage** (échec explicite
  `host environment variable ... is not set` sinon). Asymétrie réelle :
  rotation = live, ajout = prochain démarrage.
- **Suppression** : le placeholder reste dans l'environnement invité et
  passe **littéralement** vers l'hôte anciennement autorisé (pas de valeur,
  mais plus de protection non plus — l'application invité voit un échec
  d'authentification côté serveur, pas un refus local).
- **Redémarrage** : un secret `kind: env` est résolu depuis l'environnement
  hôte au démarrage ; le redémarrage échoue si la variable hôte a disparu.

### Surface SDK / CLI

- `SecretEnvOptions{Allow, Passthrough, Placeholder, RequireTLSIdentity,
  Substitution{Headers, Query, Body}, ViolationAction}` ;
  `ViolationAction` = `block` | `block-and-log` (défaut sandbox) |
  `block-and-terminate`.
- `ModifyOptions.Secrets map[string]SecretModifySpec` avec `Env`/`Value`/
  `Store` **mutuellement exclusifs** ; `Store` = référence à un secret
  store hôte (`{"kind": "store", "reference": ...}`). **Aucune fabrique
  `Secret.Store` n'existe côté création dans le SDK Go v0.7.2** (seul le
  chemin modify sérialise `kind: store`), et aucune CLI keyring n'a été
  trouvée (`security find-generic-password` macOS fonctionne, mais msb
  0.7.0 n'expose pas de backend keyring) : le stockage keyring n'est pas
  disponible aujourd'hui, seulement spécifié dans le format wire.
- `msb modify <name> --secret NAME@HOST[,HOST...] --secret-rm NAME
  [--next-start|--restart|--dry-run]`.
- Écart de version : CLI/runtime 0.7.0 sur l'hôte de test, SDK `go.mod`
  v0.7.2, le plan dit « Microsandbox 0.7.2 ». Les deux partagent le même
  format de config sur les champs testés ; à re-vérifier à la montée
  (#60).

## Décisions pour P09/P10/P13

1. **Basic auth et Context7 passent la porte.** Le gate P02 est levé :
   Git HTTPS Basic (preuve réelle clone+push) et Context7 (Bearer/API-key
   en-tête) sont protégeables par le proxy. P13 peut provisionner `gh` et
   Git HTTPS via secret proxy ; les liaisons GitHub/Context7 de P09
   peuvent s'exposer.
2. **P09 doit migrer codebox du chemin SDK Value vers le chemin
   référence** (`SecretModifySpec{Env: ...}` / source `kind: env`, ou
   `Store` quand un backend existera) : la persistance hôte de la valeur
   brute en base 0644 contredit l'objectif du chantier identifiants. Tant
   que le chemin Value est utilisé, la documentation ne doit pas présenter
   le mécanisme comme « zéro valeur brute côté hôte ».
3. **La rotation est le seul cycle live.** P09 expose la rotation
   immédiate sur invité en cours d'exécution ; l'ajout d'une nouvelle
   liaison est une opération `next_start` (redémarrage requis), à
   présenter comme telle dans l'UX.
4. **La suppression doit être signalée.** Après `--secret-rm`, l'invité
   garde un placeholder qui part littéralement sur le réseau : P09 doit
   avertir (ou exiger un redémarrage) pour éviter qu'une application invité
   n'envoie un placeholder au serveur réel, révélant le pattern de
   placeholder à un tiers.
5. **Query et body ne sont pas des canaux de substitution.** Les
   configurations P09/P10 ne doivent jamais placer un placeholder dans une
   query ou un corps : c'est une violation bloquante par défaut. Si un
   service l'exige, `Substitution{Query/Body}` existe mais doit être une
   décision explicite par destination.
6. **L'autorisation est par destination résolue par le moteur.** P09 doit
   provisionner les destinations autorisées comme noms DNS réels (pas
   d'alias invité), et documenter que l'entrée `/etc/hosts` invité ne
   suffit pas.

## Limites

- Mesures sur macOS (Apple Silicon, libkrun) uniquement ; les familles
  d'OS invitées autres qu'Alpine et les backends Linux (KVM) restent à
  caractériser (P03 couvre les invités ; le runtime hôte Linux est hors
  périmètre P02).
- Le canal body et le HTTP non-TLS n'ont pas été caractérisés (voir
  README du harnais).
- Context7 : conclusion de source + canal d'en-tête prouvé, pas un appel
  Context7 réel avec clé réelle.
- La preuve Git utilise un PAT classique ; les jetons fine-grained et
  l'installation App suivent le même transport (Basic) mais n'ont pas
  été testés individuellement.
- Le harnais rejouable exige le laboratoire local (DNS + TLS + CA) et
  n'est pas exécutable en CI tel quel ; c'est un harnais de reproduction
  manuelle, comme P01 pour ses cas réseau.

## Addendum P09 (2026-09-24) : migration effective vers le chemin référence

P09 a appliqué la décision 2 : plus aucun chemin codebox n'utilise la
source `Value` du SDK.

- **Création.** La surface create du SDK n'accepte que des valeurs en ligne
  ; chaque liaison y est enregistrée avec une sentinelle inerte
  (`$MSB_BOOTSTRAP_UNSET`, reconnaissable, sans aucun pouvoir), puis
  immédiatement tournée vers la référence d'environnement hôte
  (`SecretModifySpec{Env: ...}`, politique `no_restart` — le seul cycle live)
  avant que le script de démarrage ne s'exécute. Un échec de rotation
  supprime le sandbox incomplet plutôt que de laisser la sentinelle.
- **Rafraîchissement (next_start).** `ModifyNextStart` ré-enregistre chaque
  liaison comme référence `{"kind":"env","var":...}` ; la valeur est résolue
  depuis l'environnement du processus codebox au moment de l'application
  et du démarrage, jamais persistée.
- **Résolution à l'instant de l'opération.** La valeur (credentialRef >
  environnement legacy > magasin P08) n'existe que dans une variable de
  transport dédiée (`CODEBOX_HOST_*`), publiée le temps de l'appel SDK
  puis retirée (la valeur préexistante est restaurée). La spec SDK ne porte
  que des métadonnées : aucune erreur SDK ne peut contenir de secret.
- **Liaisons multiples.** Le registre (albert requis ; github, context7
  optionnels et approuvés projet par projet, enregistrement local hôte non
  versionné) impose des hôtes autorisés disjoints : une liaison ne peut pas
  être substituée vers la destination d'une autre.
- **Révocation.** `auth remove` supprime la liaison proxy à chaud sur les
  instances en cours d'exécution (décision 4 : l'avertissement sur le
  placeholder pendant est émis) et la référence persistée sur les instances
  arrêtées. Les instances dont la clé venait de l'environnement sont
  ignorées (enregistrement `boundCredentials` de l'état de réconciliation).
  Tart et agent-vm, sans proxy, bloquent la suppression tant qu'une
  instance tourne et exigent `--acknowledge-guest-credentials` au
  démarrage.

  **Révocation par entrée, pas par nom de liaison (ajouté après revue Codex).**
  Un `credentialRef` peut faire résoudre la liaison Albert depuis une entrée
  de magasin nommée autrement (`credentialRef: "github"` → `ALBERT_API_KEY`
  côté invité). La révocation adresse donc des entrées : l'état persiste
  `entrée@magasin#liaison`, et c'est la liaison invitée effectivement
  alimentée qui est retirée. Le garde-fou des runtimes en clair (Tart,
  agent-vm) se déclenche sur la **liaison** et non sur le nom de l'entrée :
  supprimer une entrée qui alimente Albert est bloqué même si elle ne
  s'appelle pas `albert`.
  - Le magasin qui a répondu fait partie de l'empreinte du jeu de liaisons :
    sans lui, perdre l'entrée native alors que le repli la détient encore
    laisserait la révision inchangée et l'instance saine prendrait le chemin
    no-op, sans jamais se relier au repli.
  - Une opération de réconciliation dépendante des identifiants
    (`refresh-credentials`, `create`, `start-vm`) est **refusée** quand le
    jeu n'a pas pu être résolu : dériver l'ensemble désiré du vide et
    l'appliquer transformerait « réutiliser les références persistées » en
    leur suppression. Le journal est conservé pour reprendre après
    résolution ; `restart-vm` et le relancement du backend restent
    disponibles, leurs démarrages re-résolvant les références persistées.
- Les tests de contrat P02 qui épinglaient l'écart (persistance de la
  valeur brute) ont été basculés en tests P09 qui épinglent l'absence de
  valeur brute.

## Requalification v0.7.3 (2026-10-02)

Suite à la montée du runtime Microsandbox de v0.7.2 vers v0.7.3 (issue
#104, PR #107), les matrices d'intégration ont été rejouées sur Mac
Apple Silicon avec le laboratoire réseau local corrigé.

**Résultats :**

- **Matrice credentials (P02) :** 14/15 passent. Le seul échec (T8a)
  était un faux négatif causé par le serveur écho du laboratoire, qui
  redirigeait stdout/stderr dans le même fichier que le journal JSON
  (`requests.log`). Les connexions bloquées par le proxy généraient des
  tracebacks `ConnectionResetError` qui écrasaient les entrées JSON
  précédentes. Le transport lui-même était correct (T4, T7, T9, T10,
  T11b passent). Le laboratoire a été corrigé pour écrire le journal
  JSON dans un fichier dédié (`requests.jsonl`).

- **Matrice guest-capabilities (P03) :** 16/16 passent (T1–T10c),
  incluant Node/npm, OpenCode, Chromium headless, Playwright MCP, et
  les clones Git invité. L'assertion T7a a été mise à jour pour
  chercher le titre (« Example Domain ») au lieu de `<h1>` disparu du
  site example.com.

**État de qualification :** le résultat initial 14/15 ne qualifiait pas
entièrement le transport des identifiants (aucun rejeu complet après la
séparation des journaux ; la revue Codex de #109 a relevé un journal JSON
conservé entre exécutions et un contrat de préparation incompatible).
Après correction (réinitialisation du journal au démarrage du serveur,
serveur TLS bundlé géré par `run.sh`, certificats avec extensions
explicites), le rejeu complet sur macOS (msb 0.7.3) donne **15/15**
deux fois de suite, y compris avec un journal volontairement pollué
avant la seconde exécution. La matrice des capacités invité reste 16/16.
Le transport des identifiants est qualifié sur v0.7.3.

## Requalification transport des identifiants (#205, 2026-10-22)

Le harnais épinglait encore `msb` v0.7.3 alors que codebox embarque
v0.7.6. Il répondait aussi REFUSED aux requêtes AAAA pour les deux noms
du laboratoire, ce qui faisait abandonner les requêtes par le `wget`
BusyBox de l'invité Alpine.

**Corrections et interprétation de T6 :**

- `pin.json` suit maintenant le runtime embarqué v0.7.6.
- Le DNS du laboratoire répond NODATA (NOERROR, aucune réponse) aux
  requêtes AAAA de `p02lab.test` et `evil.test`. Les autres noms restent
  refusés pour déclencher le repli DNS.
- La substitution query reste désactivée par défaut. Depuis v0.7.5, un
  placeholder peut être transmis littéralement à un hôte autorisé quand
  cette zone n'est pas activée. T6 vérifie ce comportement : le serveur
  reçoit `$MSB_P02_CANARY` dans la query, tandis que l'en-tête du même
  appel contient le canari substitué. Le canari est synthétique ; aucune
  vraie valeur n'est placée dans la query.
- Ce résultat ne signifie pas que la requête est bloquée. Il confirme que
  le secret réel n'est pas substitué dans la query ; le placeholder, lui,
  est visible par le serveur autorisé.

**Résultats macOS arm64 :**

- Runtime v0.7.6, celui actuellement embarqué : 16/16 assertions.
- Runtime amont v0.7.8, testé à part comme candidat : 16/16 assertions.

Les deux transcripts sont dans
`tests/integration/credentials/evidence/`. L'application reste épinglée
sur v0.7.6 : v0.7.8 n'apporte pas de correction documentée à ce cas T6,
et ce ticket ne constitue pas une requalification des autres parcours SDK.
La suite est qualifiée sur le runtime livré v0.7.6 ; le test sur v0.7.8
confirme que le comportement documenté persiste dans la release amont
récente.
