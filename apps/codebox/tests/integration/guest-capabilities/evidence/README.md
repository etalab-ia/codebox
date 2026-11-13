# Preuves de qualification guest-capabilities (P03)

Ces transcripts documentent la matrice P03 sur MSB 0.7.8, le runtime livré par
`main`. Ils couvrent les capacités de base d'un invité Microsandbox, pilotées
par le CLI `msb`. Ils ne qualifient ni le transport des identifiants P02, ni
les parcours intégrés à codebox, ni l'exécution du SDK Go.

## Résultats du 27 octobre 2026

Les tests ont été exécutés sur MacBook-Pro-Luis.local, sous macOS 27.0.1
(arm64), avec libkrun. Deux passages sont conservés : la qualification
courante sur 0.7.8, puis l'historique sur 0.7.6.

### Qualification courante : runtime 0.7.8

Harnais issu de `e1484a7fd27ee5252c315403858d74c733c0da24`.

- `darwin-arm64-msb-0.7.8-non-network.txt` : **8 réussites, 0 échec**,
  sortie 0. Le `MSB_HOME` a été vidé avant le run : T1 télécharge lui-même
  les deux images absentes, puis vérifie leurs références et l'architecture
  arm64. Le script indique explicitement que les cas réseau sont ignorés.
- `darwin-arm64-msb-0.7.8-network.txt` : **18 réussites, 0 échec**, sortie 0.
  `MSB_GUEST_INTEGRATION_NETWORK=1` active les outils invités, Chromium,
  Playwright MCP et les clones Git. T10a-T10c passent depuis un clone Git
  principal.
- `darwin-arm64-msb-0.7.8-archive-failure.txt` : **échec de préparation
  attendu**, sortie 2. T1-T9b passent depuis une copie sans `.git`, puis le
  prévol T10 affiche que le clone principal est introuvable. Le diagnostic
  apparaît avant tout arrêt ou retrait de la sandbox T10.
- `darwin-arm64-msb-0.7.8-t1-precondition.txt` : trace historique du défaut
  corrigé. Sur un `MSB_HOME` neuf, l'ancien T1 échouait avec
  `image not found: alpine` et sortait 2. `ensure_image` a été ajouté pour
  que T1 télécharge une image absente au lieu d'exiger un cache préexistant.

T1 comporte trois assertions : référence Alpine, référence Debian et
architecture arm64 des deux images. Il ne valide pas leur taille. Les comptes
8 et 18 incluent ces trois assertions.

### Historique : runtime 0.7.6

Harnais issu de `7f8b3dec70c7738989a4f97a62f2ad24d228194f`. Ce runtime était
livré lors de l'ouverture de l'issue #235 ; il a depuis été remplacé par
0.7.8. Les fichiers `darwin-arm64-msb-0.7.6-*.txt` conservent ces mesures.
Elles ne qualifient pas le runtime actuel.

## Environnement et versions

Les transcripts de qualification et de prévol conservent la commande
complète, dont `MSB_BIN`, `MSB_HOME`, `MSB_PATH` et `MSB_LIBKRUNFW_PATH`, la
version brute du binaire et `msb doctor`. C'est le cas des trois runs 0.7.8 et
des trois runs 0.7.6. La trace `t1-precondition` est volontairement plus
brève : elle conserve la sortie brute du harnais et la provenance, sans
republier les blocs de version de la même session.

Le runtime 0.7.8 installé pour la qualification courante a le SHA-256
`5dbe0d921c6b6f807d2c59f87e0a3c3a648424871b6bee513b837a8c600a2493`, identique
à celui gravé dans codebox ; l'historique 0.7.6 porte `a4f1722f…`. Dans les
deux cas `msb doctor` confirme libkrunfw et Apple Silicon.

La sortie brute `go list -m` déclare le SDK Go de la révision testée (v0.7.8
pour la qualification courante). La matrice P03 appelle le CLI : elle
n'exécute pas ce SDK. Le `MSB_HOME` temporaire évite de modifier le runtime
0.7.9 déjà installé sur le Mac.

## Nettoyage et limites

Les trois transcripts incluent la sortie brute finale de `msb list` : aucune
sandbox ne subsiste. Les tests n'ont touché ni les dépôts personnels, ni le
`~/.microsandbox` de l'utilisateur.

Le run réseau de qualification utilise un clone principal temporaire pour le
montage hôte de T10. Son harnais est byte-identique à la révision déclarée,
indépendamment du HEAD de ce clone temporaire. Un worktree lié est accepté
lorsque son clone principal reste accessible.

Les mesures de temps sont celles d'un poste de développement, pas des
objectifs de performance. Les limites antérieures sur SIGINT en vrai terminal,
x86_64, les sous-modules et le navigateur auto-téléchargé restent applicables.
Cette matrice ne requalifie pas le profil navigateur P16.

## Historique de la revue

La première proposition comptait 6/6 et 16/16 en incluant une réussite T1
inconditionnelle. La revue Kimi a montré que ce résultat ne vérifiait pas la
présence des images. Elle a aussi relevé la traçabilité incomplète de
l'environnement et l'échec silencieux d'une archive sans `.git`. Ces résultats
ont été remplacés par les trois transcripts ci-dessus. Le commit `011a681`
dans l'historique de la PR conserve la première proposition, mais il n'est
plus la référence de qualification.
