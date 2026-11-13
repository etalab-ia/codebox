# Harnais de qualification des capacités invité (P03)

Rejoue la qualification Alpine vs Debian, navigateur, MCP, PTY et clone
invité (décision `docs/decisions/2026-09-24-microsandbox-guest-capabilities.md`)
contre un vrai runtime Microsandbox épinglé, sur macOS (libkrun, arm64).
Exécute le script depuis un checkout Git du dépôt : le cas T10 monte le clone
principal en lecture seule dans l'invité. Une archive sans `.git` ne remplit
pas ce prérequis. Un worktree lié est accepté si son clone principal reste
accessible.

T1 télécharge une image absente de `$MSB_HOME`. Le harnais fonctionne donc sur
un runtime neuf, sans exiger un cache d'images préexistant.

## Usage

```sh
# cas non réseau (images, cycle de vie, PTY) — rapide
tests/integration/guest-capabilities/run.sh

# cas réseau (installation d'outils, navigateur, MCP, clone) — opt-in
MSB_GUEST_INTEGRATION_NETWORK=1 tests/integration/guest-capabilities/run.sh
```

Le harnais crée deux sandboxes jetables `p03-guest-harness-{alpine,debian}`,
exécute les cas T1-T10, puis les supprime. Un échec de **préparation** sort
avec le code 2 ; un échec d'assertion sort non-zéro avec le nom du cas.

## Épinglage

Versions épinglées dans `pin.json` : CLI msb, SDK Go, images invité,
OpenCode et Node. Toute montée de version reproduit la matrice complète et
met à jour le décision record. Les transcripts macOS sont conservés dans
`evidence/` avec la révision, l'environnement et les résultats de chaque run.

## Notes de cas

- **T4 (PTY)** s'exécute sous `script` (pseudo-terminal hôte) : `msb run
  --tty` propage la taille du terminal (observé `24 80` hors pty, `40 120`
  sous pty configurée). `msb exec` est non-TTY par défaut ; `msb ssh` non
  interactif est non-TTY aussi.
- **T7/T8** : la capture d'écran est vérifiée non vide en poids, pas en
  contenu pixel-par-pixel ; la session P02 a produit une capture relue
  visuellement (`example.com`, `Example Domain`).
- **T8/T10** : le navigateur Playwright MCP utilise le chromium système via
  `--executable-path` ; le navigateur auto-téléchargé de Playwright
  (`chrome-for-testing`) n'est pas utilisé (l'installation n'est pas
  rejouée — voir la limite correspondante dans le décision record).
- **T10** : l'origine d'un clone local est le chemin source (`/tmp/...`) ;
  P22 doit réécrire l'origine vers l'URL distante après clone.
