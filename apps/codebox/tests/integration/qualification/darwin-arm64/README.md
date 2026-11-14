# Qualification live — macOS arm64 (Apple Silicon)

Exécutée le 2026-10-16 sur un **Scaleway Mac mini M4** (`jc-qualif-mac1`,
macOS Tahoe 26.7.1, Apple Virtualization.framework, machine nue sans
hyperviseur, louée à l'heure pour la qualification). Source qualifiée :
`db9b8fcd2d270f32f8b5848b1a6586a1e15bc42d` (v0.10.0). Chaque transcript
porte une ligne `META ... source=<sha>` ; les transcripts du référentiel
d'identifiants portent `revision: <sha>`.

## Résultat

**La plateforme est qualifiée pour le runtime Microsandbox et le trousseau
macOS natif.** Le runtime Tart est **bloqué par l'hôte** (contrainte
environnement, pas défaut produit — voir ci-dessous). agent-vm (Lima)
fonctionne avec l'initialisation lente documentée.

## Fichiers

| Fichier | Étape du protocole | Résultat |
|---|---|---|
| `30-mac-installer.txt` | Installation officielle (`scripts/install.sh` pipe) | v0.10.0 installé |
| `31-mac-auth-doctor.txt` | `auth add` + `setup doctor` | **première tentative refusée** (`AUTH_EXIT: 1`, -25308 : trousseau verrouillée, interaction impossible en headless) ; doctor structurel OK |
| `31b-mac-auth-after-unlock.txt` | `auth status` après `security unlock-keychain` | trousseau dédiée déverrouillée : `albert: stored`, sortie 0 |
| `31c-mac-setup-doctor.txt` | `setup doctor` texte + `--json` complets (cohérence, absence de valeur secrète) | les deux rapports complets et cohérents, `EXIT: 0` chacun (31 ne conservait qu'un extrait tronqué sans statut) ; contrôle de fuite consigné dans le fichier : 11 valeurs dotenv testées par correspondance exacte, 0 occurrence |
| `32-mac-init-start.txt`, `32b-mac-start.txt` | Porte microVM (init, premier `start`, `check`) | microVM démarrée (~9 s), `check` OK |
| `32c-mac-msb-doctor.txt` | `doctor --microsandbox` **après** installation du runtime | toutes les vérifications au vert (msb, libkrunfw.5.dylib, reflink, Apple silicon), `DOCTOR_EXIT: 0` |
| `33-mac-common.txt` | Filtre transfert, export→review→apply→sync, cycle stop/start, état | filtres dotenv/gitignore/gitleaks OK ; round trip OK ; redémarrage ~1,4 s |
| `34-mac-secrets.txt`, `34b-mac-delivery.txt`, `34c-mac-substitution.txt` | Secrets : import, classes, livraison, substitution, rotation, révocation, verrou | matrice complète sur trousseau native |
| `34d-mac-locked-store.txt` | Magasin verrouillé : diagnostic explicite + état des liaisons | trousseau qualif verrouillée par `security lock-keychain` : le démarrage **échoue** avec `cannot apply the credential operations of jc-proj-d-05d90 ([start-vm]): the credential set could not be resolved … nothing was changed` (avertissement amont `keychain get: denied: exit 36`), sortie 1 ; après déverrouillage, les 4 liaisons approuvées sont **identiques** à la ligne de base — aucune désinscription silencieuse |
| `37-mac-tui-terminal.txt` | TUI dans l'invité + matrice terminal mode ligne (40/200 cols, tailles vérifiées) | opencode visible dans l'invité ; confirmations correctes |
| `46c-mac-terminal-interactive.txt` + `46c-*.typescript` + `46c-input.log` | matrice terminal **mode interactif huh** (pty `script`, Ctrl-C à 40/200 cols, annulation « n », contrôle positif « y ») | Ctrl-C annule l'invite proprement aux deux largeurs (`user aborted`, sortie interne 1 — le script(1) macOS ne propage pas le statut, l'exit interne est consigné dans le typescript) ; « n » annule sans rien persister ; le contrôle « y » stocke et prouve que le contrôle de persistance discrimine (46/46b remplacés : exit 127 faute de `timeout`, puis exit externe script(1) non probant). **Preuve d'entrée** : `46c-input.log` consigne, pour chaque exécution, l'horodatage, le délai et les octets **exacts** écrits sur l'entrée standard du pty (`bytes=03` pour Ctrl-C, `bytes=6e 0d` pour « n », `bytes=79 0d` pour le « y ») ; le transcript seul ne suffit pas, car il n'enregistre que la sortie et, une fois huh en mode brut (ISIG désactivé), l'octet ETX n'est pas réaffiché. Pas d'invite d'approbation en v0.10.0 (#209) ; un `approve` en échec laisse un état partiel (#210) |
| `38-lab-version.txt`, `39-*.txt` | Laboratoire identifiants réseau (matrice T1-T11) | **14/15** avec pin et dns corrigés — voir #205 |
| `42-agentvm.txt` | agent-vm (Lima) doctor/start/health/stop | VM démarre, OpenCode sain après ~3 min d'initialisation |
| `42f-agentvm-lifecycle.txt` | agent-vm : cycle de vie du modèle de base et persistance de l'état invité | `agent-vm-base` présent **exactement une fois** avant et après le scénario (`BASE_TEMPLATE_COUNT: 1` — construit une seule fois au premier démarrage du 2026-10-16, jamais reconstruit : démarrages en 6-7 s sans étape de build) ; marqueur d'état écrit dans le home invité (`/home/m1.guest`), stop/start complet, **marqueur relu à l'identique après redémarrage** (`STATE_PERSISTENCE: MATCH`), santé OpenCode verte aux deux amorçages |
| `43-44-import-team.txt` | Import Albert Code + cycle dépôt d'équipe | preview/apply/idempotence/préservation OK |
| `35-tart-doctor.txt`, `tart-start2.log`, `tart-start3.log`, `tart-manual-run.log` | Runtime Tart | **bloqué par l'hôte** : VZ refuse la création de la clé hôte (trousseau login non déverrouillable en headless) — voir #206 |
| `tart-start*.log`, `tart-manual-run.log` | journaux bruts Tart | conservés pour le diagnostic |

## Points notables

- **Trousseau** : sur un hôte headless, le trousseau `login` n'est pas
  déverrouillable (mot de passe inconnu, hôte provisionné). La
  qualification a utilisé un trousseau dédié `qualif.keychain-db`
  (créé, déverrouillé, auto-verrouillage désactivé), défini comme
  trousseau par défaut. Toutes les opérations du produit (auth, secrets,
  matrice du référentiel) passent par ce trousseau natif.
- **MicroVM** : premier démarrage ~9 s (l'affichage tronqué par `tail`
  masque les messages d'installation du runtime ; la preuve du boot est
  `msb list` + `check`).
- **Tart** : image `macos-tahoe-base` clonée (4 min), puis échec de
  démarrage — Virtualization.framework ne peut pas créer la clé hôte de
  la VM sans trousseau de session déverrouillé (`VZErrorDomain -9`).
  Diagnostic produit à améliorer (#206) ; contournement : session GUI.
- **agent-vm (Lima)** : pilote `vz`, aarch64. Le serveur OpenCode met
  **~3 minutes** à écouter après le démarrage de la VM ; un `check`
  immédiat échoue (connexion refusée) puis réussit une fois le serveur
  prêt (`{"healthy":true,"version":"1.19.3"}`). À documenter pour les
  utilisateurs (et éventuellement délai d'attente côté produit).
- **Laboratoire identifiants (#205, requalifié le 2026-10-22)** : le pin
  v0.7.3 et la réponse REFUSED aux AAAA sont corrigés. T6 reflète désormais
  le comportement documenté depuis msb v0.7.5 : un placeholder non substitué
  peut être livré littéralement à l'hôte autorisé. Les matrices macOS
  passent 16/16 sur v0.7.6 (runtime embarqué) et 16/16 sur v0.7.8
  (candidat amont). Transcripts :
  `tests/integration/credentials/evidence/darwin-arm64-msb-0.7.6.txt` et
  `tests/integration/credentials/evidence/darwin-arm64-msb-0.7.8.txt`.
- **Capacités invité P03 (#235, requalifiées le 2026-10-27)** : la matrice
  passe 8/8 sans réseau et 18/18 avec `MSB_GUEST_INTEGRATION_NETWORK=1`
  sur MSB 0.7.8, SDK v0.7.8 déclaré, macOS 27.0.1 arm64 et libkrun. Elle a été
  exécutée sur MacBook-Pro-Luis.local, avec un runtime temporaire isolé et un
  `MSB_HOME` vidé avant le run non réseau, ce qui vérifie que T1 télécharge
  les images manquantes. T10 nécessite un checkout Git ; une archive sans
  `.git` échoue au prévol avec la sortie 2 et un diagnostic explicite. Les
  preuves, dont la sortie brute finale de `msb list`, sont dans
  `tests/integration/guest-capabilities/evidence/`. Cette matrice CLI P03
  reste distincte du transport des identifiants P02 ci-dessus et des parcours
  intégrés à l'application. Elle ne requalifie pas le profil navigateur P16.
  Les mesures 0.7.6 conservées dans le même dossier sont historiques.

## Hôte

Machine distante gérée par Scaleway (Apple Silicon à l'heure). L'accès
SSH est par clé dédiée jetable ; le mot de passe du trousseau `login`
n'est pas connu (provisionnement), ce qui motive le trousseau dédié.

## Défauts et observations associés

- #205 — harnais credentials : pin périmé, AAAA REFUSED, T6 changé
- #206 — Tart : le timeout « guest agent » masque un refus VZ (trousseau)
- #201 — résolution des identifiants native-only (contournée ici en
  déverrouillant le trousseau dans la même session)
