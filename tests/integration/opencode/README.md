# Harnais d'intégration OpenCode (P01)

Rejoue la matrice de caractérisation du contrat de configuration OpenCode
(décision `docs/decisions/2026-09-23-opencode-configuration-contract.md`)
contre un vrai processus OpenCode épinglé, hors microVM.

## Usage

```bash
# cas non réseau (fusion, découverte, JSONC, plugins) — sans clé
tests/integration/opencode/run.sh

# Ajouter une skill réellement résolue/verrouillée par just-code (P14)
JUST_CODE_TEST_SKILL_ARCHIVE=/chemin/cache/rgaa.tar.gz JUST_CODE_TEST_SKILL_NAME=rgaa \
  tests/integration/opencode/run.sh

# cas réseau (sélection effective de modèle via Albert) — opt-in
OPENCODE_INTEGRATION_NETWORK=1 ALBERT_API_KEY=... tests/integration/opencode/run.sh
```

Le harnais installe `@opencode/cli@2.2.5` (version de `pin.json`) dans un répertoire temporaire npm. `OPENCODE_INTEGRATION_BINARY=/chemin/opencode` permet de réutiliser un binaire déjà téléchargé ; sa version reste vérifiée. Il construit des projets synthétiques sous un HOME/XDG isolé, utilise `--standalone` pour les sondes courtes et arrête chaque serveur `serve` après le test. Un échec d'assertion sort non-zéro avec le nom du cas.

La matrice v2 vérifie `/api/config` (sources normalisées, pas un objet fusionné), les providers et métadonnées effectifs via `/api/provider` et `/api/model`, les skills via `/api/skill`, et un serveur authentifié via `/api/info`, `/api/location` et `/api/session`. Le mot de passe synthétique est transmis par environnement, jamais dans les arguments. Elle couvre les options `--server`, `--standalone`, la reprise et les permissions normalisées. Les anciennes sondes v1 supprimées sont signalées explicitement comme non applicables ; la décision P01 reste un constat historique et n'est pas réécrite.
Le cas P14 optionnel copie une archive résolue/vérifiée par just-code dans le
répertoire global de l'HOME isolé, puis confirme que le vrai processus OpenCode
ne découvre que les skills de base et la sélection prévue.

## Épinglage

La version d'OpenCode testée est épinglée dans `pin.json`. Le harnais
échoue si la version installée ne correspond pas. Une montée de version
d'OpenCode doit reproduire la matrice complète et documenter les nouveaux contrats opérationnels sans réécrire les qualifications historiques.

## Relation avec l'invité

Ce harnais qualifie le comportement d'OpenCode lui-même, pas l'image invité
`ghcr.io/anomalyco/opencode` : la version embarquée par l'image peut différer.
Le bootstrap Microsandbox installe désormais le CLI v2 épinglé par-dessus l'image de base. Les tests Go vérifient cette préparation et le refus des scripts invités v1 immuables. Une qualification native séparée reste nécessaire pour prouver le démarrage de la microVM.
