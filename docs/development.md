# Développement

Ce document décrit l'architecture interne du CLI, l'outillage de développement et le pipeline de publication. Pour l'installation et l'utilisation, voir le [README](../README.md).

## Configuration locale

Depuis un checkout des sources, pars du modèle commenté puis renseigne au minimum `ALBERT_API_KEY` :

```bash
cp .env.example .env
```

La référence complète des variables et de leurs règles de priorité se trouve dans [config.md](config.md).

## Portage Go

Le CLI est un binaire Go unique (`cmd/just-code`) qui remplace entièrement le `justfile`. Il orchestre les deux runtimes via une bibliothèque testée (`internal/justcode`) et le SDK Go Microsandbox, sans dépendre d'une commande `msb` externe.

**Binaire autonome.** La configuration OpenCode et le script de démarrage Microsandbox vivent dans `assets/` et sont embarqués dans le binaire via `go:embed`. Ils sont transmis directement au SDK sans fichier de configuration temporaire. Le runtime natif téléchargé reste séparé sous `~/.microsandbox/` (ou `$MSB_HOME`). Il provient de la release amont immuable `superradcompany/microsandbox` : son URL et ses empreintes SHA-256 sont compilées dans `just-code`, l'archive est vérifiée avant décompression, puis la paire installée est validée via `ResolveRuntime()`.

**Pas de script shell.** Le bootstrap invité Tart (clampage MTU, installation d'OpenCode, `exec opencode serve`) est du code Go dans le même binaire, exposé sous la sous-commande interne `__guest-bootstrap`. La VM macOS étant elle aussi en arm64, le CLI copie son propre binaire dans le partage en lecture seule, puis le copie sur le disque local de l'invité (l'exécution directe depuis le partage virtiofs n'est pas fiable) avant de l'exécuter.

Les trois régressions shell de l'ancien `justfile` sont corrigées et couvertes par `go test` :

- **Délai de santé à horloge murale** : la boucle attend une échéance mesurée en temps réel (300 s par défaut, `JUST_CODE_START_TIMEOUT` en secondes), avec un timeout de 5 s par requête (une connexion bloquée ne contourne plus la limite).
- **Mot de passe vide préservé** : `OPENCODE_SERVER_PASSWORD=""` ne retombe pas silencieusement sur `albert-dev-pass`. Depuis OpenCode v2, le démarrage backend refuse cette valeur explicitement : un serveur authentifié est obligatoire.
- **Surface des flags** : `--microsandbox` et `--tart` sont reconnus (flag explicite prioritaire sur `RUNTIME`), et les messages d'erreur reflètent exactement cette surface.

Défauts découverts ensuite et corrigés dans le même esprit :

- **Auto-réparation du backend Microsandbox** : découverte et cycle de vie via le SDK, relance de l'entrée de conteneur quand la VM tourne sans backend, avec nouvelle tentative bornée pendant que l'agent invité démarre.
- **Clé API confinée sur l'hôte** : la valeur est remise directement au SDK et n'entre jamais dans l'environnement invité ; le proxy réseau ne la substitue que pour `albert.api.etalab.gouv.fr`. Elle est rafraîchie lors du redémarrage d'un sandbox existant.
- **Montages figés à la création** : `WORKSPACE_DIR` remplace `PROJECT_DIR` (déprécié, encore honoré), et Microsandbox avertit quand le montage détecté diffère de la configuration.
- **Pré-vol `opencode`** : le CLI de l'hôte est vérifié avant de démarrer un runtime, avec la commande d'installation.
- **Validation différée de `JUST_CODE_START_TIMEOUT`** : une valeur malformée bloque la commande d'attachement, pas `stop`, `clean`, `logs`, `doctor` ni `check`.
- **Runtime Microsandbox vérifié** : un runtime déjà présent n'est accepté comme installation gérée que si son marqueur de provenance correspond à l'empreinte compilée. Les installations antérieures sans marqueur sont remplacées depuis la release amont avant toute exécution de `msb`. `MSB_PATH` et `MSB_LIBKRUNFW_PATH` restent une alternative explicite pour un provisionnement administré.

Contrats comportementaux reproduits : secrets absents des arguments de processus ; isolation par préfixe `opencode-` ; endpoints de santé ; clampage de MTU (1280-1500 ou `auto`) validé sur l'hôte avant le boot de la VM ; relance du backend (SIGTERM puis SIGKILL après 10 sondes) ; détection des runtimes actifs et résolution de conflits.

Le SDK embarque une bibliothèque FFI propre à chaque plateforme et nécessite CGO. Les binaires de publication sont donc construits sur des runners natifs pour macOS arm64, Linux amd64/arm64 et Windows amd64/arm64. macOS Intel n'est pas publié : le SDK Microsandbox 0.7.6 ne fournit pas de bibliothèque FFI pour `darwin/amd64`.

```bash
go test -race ./...   # vert
go vet ./...          # propre

# Compilation sur la plateforme cible (CGO activé)
CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o just-code ./cmd/just-code
```

## Hooks pre-commit

Le dépôt embarque des hooks pre-commit. Prérequis : [pre-commit](https://pre-commit.com/#install) et [Go](https://go.dev/dl/) sur le `PATH` (pre-commit compile gitleaks depuis la version épinglée au premier commit ; gitleaks lui-même n'a pas besoin d'être installé). Après un clone :

```bash
pre-commit install --install-hooks
```

Le hook [gitleaks](https://github.com/gitleaks) scanne les changements stagés à chaque commit et bloque l'ajout de secrets (clés API, mots de passe). La version est épinglée dans `.pre-commit-config.yaml`. Le premier commit après l'installation est lent (compilation de gitleaks) ; les suivants sont quasi instantanés. Les hooks locaux ne doivent pas être contournés : toute exception demande une autorisation explicite de Luis.

Le workflow `gitleaks.yml` complète ce contrôle local sur chaque PR et chaque push sur `main`. Il vérifie l'empreinte SHA-256 du binaire avant d'analyser tout l'historique. Il n'utilise pas `gitleaks-action`, dont l'usage sur un dépôt d'organisation nécessite une licence séparée. `.gitleaks.toml` ne contient que des exclusions ciblées et expliquées : les lockfiles et les journaux de qualification restent analysés.

Les fixtures Node sous `tests/` restent des fixtures d'un projet Go : elles ne justifient ni un outillage Node racine ni un hook CVE Lite. OSV-Scanner couvre leurs lockfiles en CI.

## Intégration continue et publication

Cinq workflows GitHub Actions accompagnent le CLI :

- **`ci.yml`** (sur chaque PR, et sur `main`) : vérification du formatage (`gofmt`), `go vet`, `go test -race`, tests et compilation CGO sur chaque plateforme native prise en charge, contrat OpenCode hôte épinglé et tests de cycle de vie du serveur de laboratoire TLS. Les jobs d'intégration n'utilisent aucun identifiant et ne démarrent pas de microVM ; voir le [protocole de qualification bêta](qualification-beta.md) pour cette preuve native.
- **`gitleaks.yml`** (sur chaque PR et sur `main`) : scan complet de l'historique pour détecter les secrets, indépendamment de l'installation des hooks sur chaque poste.
- **`osv-scanner.yml`** (sur chaque PR, sur `main` et chaque semaine) : compare les vulnérabilités introduites par une PR à la branche de base et signale l'inventaire complet dans Code Scanning. Le scan local avec Go installé doit confirmer que l'analyse des appels Go fonctionne.
- **`release-please.yml`** (sur `main`) : [release-please](https://github.com/googleapis/release-please) maintient une PR de release à partir des Conventional Commits (`feat:` = minor, `fix:` = patch). Fusionner cette PR écrit le `CHANGELOG.md`, crée le tag et la release GitHub, puis construit les cinq binaires (`darwin-arm64`, `linux-arm64`, `linux-x64`, `windows-x64.exe`, `windows-arm64.exe`) sur leurs runners natifs, génère `SHA256SUMS` et les attache à la release une fois toutes les plateformes réussies. Tant que le projet n'a pas décidé de publier sa version 1.0.0, `bump-minor-pre-major` maintient aussi les changements incompatibles sur les versions mineures (`0.1.0`, `0.2.0`, etc.). Le passage à `1.0.0` se fait uniquement par décision explicite, avec `Release-As: 1.0.0` dans le commit concerné.
- **`msb-runtime-watch.yml`** (cron hebdomadaire, lundi 09:00 UTC) : surveille les releases amont du runtime Microsandbox et ouvre une issue de suivi lorsqu'une version plus récente que le SDK embarqué est publiée ; voir « Mise à jour du runtime Microsandbox » ci-dessous.

`.github/dependabot.yml` couvre chaque module Go, lockfile npm, fichier `requirements.txt`, la configuration pre-commit et les workflows. Le délai `cooldown.default-days: 7` retarde les PR de versions ordinaires, pas les correctifs de sécurité. Go n'a pas de délai de publication minimal natif équivalent à pnpm ou uv : les modules verrouillés, Dependabot et la revue restent donc nécessaires. Les mises à jour ne sont jamais fusionnées automatiquement.

Le tag de release est la source de vérité de la version : il est injecté dans le binaire via `-ldflags "-X main.version=<tag>"` (sans le préfixe `just-code-`), donc `just-code version` affiche exactement la version publiée.

Pour publier : fusionner la PR de release-please. C'est tout. Le pipeline d'artefacts peut aussi être exercé sans couper une release : `workflow_dispatch` avec un tag existant reconstruit et réattache les assets.

### Mise à jour du runtime Microsandbox

Le runtime évolue indépendamment du CLI. `just-code` télécharge les archives directement depuis la release amont `superradcompany/microsandbox`, et non depuis un miroir maison. Ce choix est délibéré : la garantie de sécurité vient de l'empreinte SHA-256 gravée dans le binaire et vérifiée avant toute décompression, donc elle est indépendante de la source. Un miroir n'ajouterait rien, et il serait même le maillon le plus faible sur l'axe « les octets peuvent-ils changer après publication », les releases amont étant immuables alors qu'une release que nous publierions ne le serait pas. La frontière de confiance est double. Les releases amont sont immuables : GitHub émet une attestation de release signée (Sigstore, signataire `https://dotcom.releases.github.com`) couvrant tous les assets, et le workflow la vérifie cryptographiquement via `gh release verify` puis source toutes ses empreintes des sujets attestés. Le `checksums.sha256` amont ne sert que de contre-vérification : il est téléversé avec les archives et pourrait être remplacé dans le même geste, donc tout désaccord avec l'attestation échoue fermé et demande une investigation humaine. Au-delà, la revue humaine des empreintes gravées dans le binaire reste la frontière finale.

**Détection (automatique, cron hebdomadaire).** Le workflow compare la dernière release amont `superradcompany/microsandbox` à la version du SDK embarqué (via le flag caché `just-code -print-msb-sdk-version`). S'il existe un `vX.Y.Z` amont plus récent que le SDK embarqué, une issue de suivi est ouverte avec les empreintes SHA-256 des cinq archives, issues de l'attestation de release vérifiée. Il ne publie rien et ne touche pas au code. L'issue alimente directement la PR de montée de version décrite ci-dessous.

**Montée de version du SDK (PR revue).** Lors d'une montée de version de `github.com/superradcompany/microsandbox/sdk/go` :

1. Mettre à jour la dépendance dans `go.mod` et confirmer la valeur renvoyée par `msb.SDKVersion()`.
2. Vérifier la release amont correspondante avant de graver quoi que ce soit : `gh release verify v<X.Y.Z> --repo superradcompany/microsandbox --format json`, puis extraire les empreintes des cinq archives depuis les sujets vérifiés. Ne jamais graver une empreinte issue du seul `checksums.sha256` amont.
3. Mettre à jour ensemble `msbRuntimeVersion`, `msbRuntimeReleaseURL` et les cinq empreintes dans `internal/justcode/msb_runtime_install.go`. L'URL suit la forme `https://github.com/superradcompany/microsandbox/releases/download/v<X.Y.Z>`. Le test `TestMSBRuntimeVersionMatchesSDK` bloque un décalage entre le SDK et le runtime.
4. Tester au minimum `go test -race ./...`, puis exécuter `just-code start --microsandbox` avec un `$MSB_HOME` vide sur une plateforme réelle pour valider le téléchargement, l'empreinte, l'extraction et `msb --version` de bout en bout. `just-code doctor --microsandbox` ne télécharge rien : il ne vérifie que le runtime déjà installé.
5. Exécuter `just-code setup doctor` sur la même plateforme et vérifier que le rapport sépare capacité hôte, runtime/VM, toolchain, provider, skills/MCP et stockage des identifiants, sans afficher de valeur secrète. `just-code setup doctor --json` expose le même rapport avec des clés stables pour automatisation.
