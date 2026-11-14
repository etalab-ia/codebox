# Qualification live — Linux arm64

Exécutée le 2026-10-16 sur un **Scaleway Mac mini M2 sous Asahi Linux**
(`jc-qualif-asahi1`, Fedora Linux Asahi Remix 42, noyau
`6.14.2-401.asahi.fc42.aarch64+16k`, `/dev/kvm` présent et utilisable,
10 cœurs / 15 Gio). Source qualifiée :
`db9b8fcd2d270f32f8b5848b1a6586a1e15bc42d` (v0.10.0). Chaque transcript
porte une ligne `META ... source=<sha>` ; les transcripts du référentiel
d'identifiants portent `revision: <sha>`.

## Choix de l'hôte

Les Instances Scaleway arm64 classiques (BASIC2-A2C-4G, Ubuntu 24.04)
**n'exposent pas de virtualisation emboîtée** (`kvm: HYP mode not
available` au boot — vérifié par sonde le 2026-10-16, instance détruite
ensuite). L'hôte Asahi est un vrai arm64 avec KVM natif (Apple M2),
ce qui en fait un hôte de qualification valide pour la ligne
« Linux arm64 » de la matrice. La distribution est Fedora (et non
Ubuntu) : les différences utiles sont notées ci-dessous.

## Résultat

**La plateforme est qualifiée pour le runtime Microsandbox et le
référentiel secret-service (gnome-keyring).** La microVM démarre sur le
noyau arm64 à pages de 16 K (point de vigilance initial, levé).

## Fichiers

| Fichier | Étape du protocole | Résultat |
|---|---|---|
| `40-inventory.txt` | Inventaire hôte + doctor runtime absent | KVM rw OK ; échec propre sans effet de bord |
| `42b-doctor-postinstall.txt` | `doctor --microsandbox` **après** installation du runtime | toutes les vérifications au vert (msb, libkrunfw, reflink, KVM), `DOCTOR_EXIT: 0` |
| `43-setup-doctor.txt` | `setup doctor` texte + `--json` (cohérence, absence de valeur secrète) | les deux rapports cohérents, `EXIT: 0` ; contrôle de fuite consigné dans le fichier (méthode, `VALUES_TESTED: 11`, `LEAK_COUNT: 0`) : aucune valeur dotenv du projet n'apparaît dans la sortie |
| `41-auth.txt` | `auth add` (file fallback) + `auth status` | identifiant stocké, statut correct |
| `50-54-common.txt` | Porte microVM, filtre, export/sync, cycle vie, TUI, import, dépôt d'équipe | premier boot **12,3 s** ; redémarrage 2,2 s ; toutes les lignes OK |
| `51b-tui.txt`, `51b-tui.typescript` | TUI dans l'invité (preuve processus) | opencode visible dans l'invité |
| `55-58-secrets.txt` | Secrets : import, classes, refus, profils, verrou | comportement identique à amd64 |
| `58b-locked-store.txt` | Magasin verrouillé : diagnostic explicite + état des liaisons | collection gnome-keyring verrouillée par `busctl` : le démarrage **échoue** avec `cannot read the guest-readable project secret APOS_VALUE (classes.env): credential not found` (étape `start-vm`, reprise possible) au lieu de poursuivre ; après déverrouillage, les 4 liaisons approuvées sont **identiques** à la ligne de base — aucune désinscription silencieuse |
| `56b-delivery.txt`, `56c-delivery.txt`, `56d-substitution.txt` | Livraison, substitution live, refus inter-hôte, rotation, révocation | marqueur seul dans l'invité, valeur réelle à httpbin, coupure SSL vers hôte non approuvé |
| `59-terminal.txt` | Matrice terminal mode ligne (40/200 cols vérifiées) ; le Ctrl-C de ce fichier est terminé par `timeout -k` et **ne vaut pas preuve** | confirmations correctes |
| `62-asahi-terminal-interactive.txt`, `62b-asahi-terminal-200-retry.txt` + `62b-*.typescript` + `62b-input.log` | Matrice terminal **mode interactif huh** (pty `script`, Ctrl-C à 40/200 cols, annulation « n », contrôle positif « y ») | Mesure avec transcript brut et dimensions vérifiées dans le transcript (`stty size` = `24 200` puis `24 40`) : une fois la négociation terminal terminée, Ctrl-C déclenche le chemin d'annulation de huh — `just-code: user aborted`, code **interne 1**, **rien de persisté** aux deux largeurs ; le contrôle positif « y » stocke les 2 variables de `.env` (code interne 0) et montre que le test de persistance discrimine. Le `exit=130` de `62` n'était qu'un statut **externe** sans transcript ni preuve de dimensions : 130 est le code d'un SIGINT reçu sur un pty encore en **mode cuit** (démontré dans `62b-cookedctrlc.typescript`), pas le chemin d'annulation de huh. Résultat identique à `darwin-arm64/46c` (exit interne 1). Pas d'invite d'approbation en v0.10.0 (#209) ; un `approve` en échec laisse un état partiel (#210). **Preuve d'entrée** : `62b-input.log` consigne, pour chaque exécution, l'horodatage, le délai et les octets **exacts** écrits sur l'entrée standard du pty (`bytes=03` pour Ctrl-C, `bytes=79 0d` pour le « y ») ; le transcript seul ne suffit pas, car `script(1)` enregistre l'entrée comme `<not executed on terminal>` et, une fois huh en mode brut (ISIG désactivé), l'octet ETX n'est pas réaffiché |
| `60-installer.txt` | Installation officielle linux-arm64 | v0.10.0, SHA256 vérifié contre la release |

Le transcript `50-54-common.txt` regroupe plusieurs étapes ; la ligne TUI
a été rejouée dans `51b-*` (le premier essai n'avait pas produit de
preuve, `script(1)` absent de la Fedora minimale — paquet
`util-linux-script`).

## Points notables

- **Noyau 16 K pages** : la compatibilité libkrunfw/microsandbox avec les
  noyaux arm64 à grandes pages était le risque identifié ; la microVM
  démarre et fonctionne normalement.
- **Distribution** : Fedora au lieu d'Ubuntu — `dnf` au lieu d'`apt`,
  `script(1)` dans `util-linux-script` (absent de la base), `gnome-keyring`
  à installer. Le produit (binaire statique Go + runtime msb téléchargé)
  est indifférent à la distribution sur les lignes qualifiées.
- **agent-vm (Lima)** : non exécuté sur cet hôte. La ligne agent-vm est
  couverte sur Linux amd64 (partiel, limité par l'hôte) et macOS arm64
  (fonctionnel).
- **Contrainte mono-instance** : confirmée ici aussi (un `start` projet
  refuse tant qu'une autre microVM du projet tourne).

## Hôte

Mac mini M2 Scaleway reformaté en Asahi Fedora 42 pour la qualification
(option « Asahi Linux » du provisionnement Scaleway). Accès SSH par clé
dédiée jetable.

## Défauts et observations associés

- Aucun défaut nouveau propre à arm64 : les comportements observés sont
  identiques à la ligne Linux amd64 (y compris #199, #202, #204).
