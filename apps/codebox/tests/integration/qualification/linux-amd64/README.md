# Qualification Linux amd64 — preuves conservées

Exécution du protocole `docs/qualification-beta.md` (étapes communes 1 à 9,
parcours secrets 1 à 7, matrice terminal/annulation, ligne installateur) sur
un hôte Linux amd64 jetable :

- Hôte : instance Scaleway DEV1-S (2 vCPU, 1,9 Gio, Ubuntu 22.04, fr-par-1),
  KVM imbriqué réel (`/dev/kvm` accessible en lecture/écriture, indicateur
  `svm` présent). La ligne Linux amd64 du protocole exige ce point : une
  microVM démarrée sans KVM réel ne vaut pas preuve.
- Révision qualifiée : `db9b8fcd2d270f32f8b5848b1a6586a1e15bc42d`
  (`just-code v0.10.0`, binaire compilé sur place en Go 1.27.2 ; la ligne
  installateur utilise le binaire officiel de la release, SHA256 vérifié).
- Date : 2026-10-16. Chaque fichier porte une ligne `META` avec l'horodatage
  et, pour les étapes qui s'y prêtent, la révision qualifiée.
  Note de traçabilité : les lignes `META` des fichiers `06` à `24` ont été
  complétées avec le champ `source` après l'exécution (même journée, même
  révision, vérifiée dans `03-setup-doctor.txt` et `23b-installer.txt`), en
  réponse à la revue de la PR ; les contenus d'exécution sont inchangés.

## Lecture des fichiers

| Fichier | Étape du protocole | Résultat |
| --- | --- | --- |
| `00-host-inventory.txt` | 1 (inventaire, KVM, versions) | conforme |
| `01-doctor-missing-runtime.txt` | 2 (doctor sans runtime, sans effet de bord) | conforme |
| `02-auth-add-albert.txt` | prérequis identifiant Albert (stdin, `--fallback`) | conforme |
| `03-setup-doctor.txt` | 2 (`setup doctor` texte et JSON, aucune valeur secrète) | conforme |
| `04-init.txt` | 4 (`init` non interactif ; refus sans `--root` puis succès) | conforme |
| `05-first-start.txt` | 4 (premier démarrage microVM : 33,8 s tout compris) | conforme |
| `06-check.txt` | 4 (`check --isolation full`) | conforme |
| `07-workspace-filter.txt` | 5 (filtre de transfert : dotenv, ignorés, gitleaks) | conforme |
| `08-export-sync.txt` | 6 (export → revue → application → `sync --force`) | conforme |
| `09-lifecycle.txt` | 7 (stop, redémarrage 5,7 s, état conservé) | conforme |
| `10-doctor-canary.txt` | 2 reprise + 5 (doctor post-install, canari `ALBERT_API_KEY=host-canary-not-a-secret` invisible depuis l'invité) | conforme |
| `11-tui-in-guest.txt`, `11-tui-launch.typescript` | 4 (le TUI tourne dans l'invité : processus `opencode` observé dans la microVM pendant l'attache) | conforme |
| `12-import-preview.txt`, `12b-import-no-init.txt`, `13-import-apply.txt` | 8 (projet préexistant, `import albert-code` aperçu/application/répétition, réglages non gérés intacts) | conforme |
| `14-team-repo.txt` | 9 (clone d'équipe : check, `update` sans changement, démarrage, export sur branche de test) | conforme |
| `15-secrets-import.txt` | secrets 1 (aperçu masqué, doublons signalés, refus non-TTY sans `--yes`, sources inchangées, `--fallback` requis et appliqué) | conforme, voir écarts ci-dessous |
| `16-bindings.txt`, `16b-bindings.txt` | secrets 2 et 3 (approbations proxy et lisible-invité, classes de valeurs livrées/refusées) | **partiel** : livraisons et refus nommés conformes, mais les classes retour-chariot et UTF-8 invalide n'ont que des erreurs d'analyse génériques, sans nommer la variable ni la raison (#208) |
| `17-delivery.txt`, `17b-delivery-after-storefix.txt` | incident : démarrage impossible avec un index importé en `--fallback` | **défaut** (#199) |
| `18-native-delivery.txt` | secrets 2 et 3 avec magasin natif : marqueur seul dans `.env`, valeurs réelles 0600 dans `classes.env`, **substitution réelle vers httpbin.org** depuis l'invité | conforme |
| `19-negative-substitution.txt` | secrets 2 (contrôle négatif : requête portant un marqueur vers un hôte non approuvé coupée par le proxy) | conforme |
| `20-lifecycle.txt`, `20b-cross-profile.txt` | secrets 4 à 6 (refus de `--clear`, rotation, révocation conformes) | **non conforme sur l'isolation inter-profils** : le démarrage no-op avec liaisons hors profil actif réussit (`CROSS_PROFILE_START_EXIT: 0` dans `20-lifecycle.txt`) au lieu d'échouer fermé ; seul le chemin de création refuse → **défaut** (#204) |
| `21-locked-store.txt` | secrets 7 (magasin indisponible : démarrage échoue explicitement, liaisons préservées) | conforme |
| `22b-terminal-matrix.txt`, `22e-terminal-interactive.txt`, `22f-terminal-interactive-retry.txt` + `22f-*.typescript` + `22f-input.log` | terminal/annulation : 40 et 200 colonnes en PTY vérifié en mode ligne (`22b`) ; mode interactif huh piloté via pty `script` (`22e`, puis re-mesuré avec transcript brut et dimensions vérifiées dans `22f`) : Ctrl-C déclenche le chemin d'annulation de huh — `just-code: user aborted`, code **interne 1**, rien de persisté — aux deux largeurs, et le contrôle positif « y » stocke (code interne 0), donc le test de persistance discrimine. Le `exit=130` de `22e` était le statut **externe** script/timeout sans transcript ; 130 est le code d'un SIGINT sur un pty encore en mode cuit, pas le chemin d'annulation de huh (`22f` est identique à `darwin-arm64/46c` et `linux-arm64/62b`). **Preuve d'entrée** : `22f-input.log` consigne, pour chaque exécution, l'horodatage, le délai et les octets **exacts** écrits sur l'entrée standard du pty (`bytes=03` pour Ctrl-C, `bytes=79 0d` pour le « y ») ; le transcript seul ne suffit pas, car il enregistre l'entrée comme `<not executed on terminal>` et, une fois huh en mode brut (ISIG désactivé), l'octet ETX n'est pas réaffiché. Deux essais de harnais piloté par python ont produit des résultats sans valeur probante (binaire inexistant dans `22c`, pty python dont l'entrée n'atteint pas huh dans `22d`) et sont retirés de la preuve ; `22` (largeur non vérifiée) est également retiré. Pas d'invite d'approbation à exercer : `bindings approve` est non interactif en v0.10.0 (#209) et un échec de résolution y laisse un état partiel (#210) | conforme sur l'import ; voir #209/#210 pour l'approbation |
| `23b-installer.txt` | installateur officiel pipe `curl \| sh`, binaire `v0.10.0`, SHA256 vérifié | conforme |
| `24-agent-vm*.txt`, `24-agent-vm-*.log` | 11 (agent-vm / Lima : doctor, construction du modèle, démarrage, arrêt) | **partiel, limité par l'hôte** |

## Écarts constatés pendant l'exécution

Chacun fait l'objet d'une issue ; ce résumé ne remplace pas leur description.

1. **#199 — Secrets importés avec `--fallback` irrésolubles au démarrage** : sur un
   hôte Linux sans session D-Bus, l'import exige `--fallback` (le magasin
   natif est verrouillé), mais `start` résout toujours le magasin natif et
   échoue. Contournement utilisé pour la suite du protocole :
   `dbus-run-session` + `gnome-keyring-daemon` déverrouillé, réimport dans le
   magasin natif.
2. **#200 — Image agent-vm par défaut invalide** : `template:debian-13` n'existe pas
   sous Lima 1.2.1 (syntaxe `template://` requise, `debian-13` déplacé sous
   `experimental/`). Contournement : `AGENT_VM_IMAGE=template://debian-12`.
3. **#201 — `start --agent-vm` ignore l'identifiant Albert du magasin de repli** :
   la résolution ne consulte que le magasin natif. Contournement : variable
   `ALBERT_API_KEY` dans l'environnement.
4. **#201 — Dimensionnement agent-vm par défaut trop grand pour un petit hôte** :
   4 Gio / 2 vCPU échoue (`Cannot allocate memory`) sur une machine de
   1,9 Gio. Contournement : `AGENT_VM_MEMORY_GB=1 AGENT_VM_CPUS=1`.
5. **#202 — Qualité des messages** : `go --version` sondé (exit 2) au lieu de
   `go version` ; erreur de réconciliation affichant `(<nil>)` au lieu de la
   cause ; `secret-service` annoncé « accessible » sur une sonde en lecture
   alors que les écritures échouent ; `--fallback` absent de la ligne d'usage
   de `secrets bindings`.
6. **#204 — Démarrage no-op tolérant une liaison hors profil actif** : sur une
   instance déjà en cours, `start` avec un profil actif différent des liaisons
   réussit (exit 0, avertissement seul) en conservant les secrets de l'ancien
   profil ; seul le chemin de création échoue explicitement.
7. **#208 — Diagnostics d'import insuffisants** : les classes retour-chariot et
   UTF-8 invalide échouent avec des erreurs d'analyse génériques qui ne
   nomment ni la variable ni la règle violée, et l'approbation ultérieure
   répond `credential not found`, indiscernable d'un jamais-importé.
8. **#209 — Pas d'invite d'approbation** : `bindings approve` est non
   interactif et son drapeau `--yes` n'est jamais lu ; la matrice
   terminal/annulation ne s'applique donc qu'à l'import en v0.10.0.
9. **#210 — État partiel après un `approve` en échec** : l'approbation est
   enregistrée et le profil actif basculé avant la vérification de
   résolution ; quand celle-ci échoue, l'état reste modifié.

## agent-vm (Lima) sur cet hôte

Le parcours agent-vm aboutit partiellement, pour des raisons de capacité de
l'hôte et non du produit :

- `doctor --agent-vm` : conforme (Lima 1.2.1 détecté ; la racine sous
  `/root` est refusée par Lima — le parcours s'exécute sous un utilisateur
  non privilégié, ce qui est une exigence de Lima, pas de just-code) ;
- construction du modèle de base : réussie avec les contournements #200
  (image) et #201 (dimensionnement : 1 Gio / 1 vCPU sur cet hôte de 1,9 Gio) ;
- démarrage de la VM gérée : réussi (5 min 31 s au premier démarrage,
  modèle inclus) ; le serveur OpenCode répond sur `/global/health` après
  ~40 s (HTTP 401, authentification requise) ;
- tenue de charge : sur 1 vCPU / 1 Gio, le serveur devient ensuite muet
  (délais dépassés sur `/provider` puis `/global/health`). `just-code check`
  ne peut pas le confirmer dans ce dimensionnement ;
- arrêt / redémarrage / arrêt : conformes, état Lima propre.

La ligne reste donc « partiel, limité par l'hôte » : la preuve de santé
interactive du backend demande un hôte plus grand (le dimensionnement par
défaut du modèle, 4 Gio / 2 vCPU, ne rentre pas dans 1,9 Gio).

## Magasin d'identifiants

Les transcripts du magasin Secret Service réel (deux exécutions
`--- PASS`, plus une exécution hors session documentant le comportement
« collection verrouillée ») vivent dans
`tests/integration/credentials/evidence/`, conformément à la convention de ce
répertoire.
