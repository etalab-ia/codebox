# Qualification bêta

Ce protocole complète les workflows GitHub Actions. Les runners hébergés
qualifient les compilations natives, les tests unitaires, les installateurs,
le contrat OpenCode hôte et les fixtures. Ils ne constituent pas une preuve
de démarrage d'une microVM sur l'hyperviseur natif.

## Prérequis

- Utiliser un checkout de la révision candidate, avec son tag/version noté.
- Utiliser une machine physique ou un hôte de virtualisation dédié, pas une
  VM imbriquée non qualifiée.
- Installer OpenCode conformément au README. Microsandbox n'a pas besoin d'être
  installé : `codebox start --microsandbox` télécharge et vérifie le runtime
  géré si nécessaire ; `codebox doctor --microsandbox` est en lecture seule
  et ne l'installe pas.
- Pour un test réel de transport Albert, utiliser une clé de test révocable.
  Ne jamais mettre une clé dans le rapport ou les artefacts de CI.
- Prévoir un checkout jetable contenant un dépôt Git synthétique pour les
  parcours qui créent ou modifient un projet.

## Parcours sur chaque hôte

1. Noter OS, architecture, matériel ou hyperviseur natif, version du CLI,
   version du runtime et date. Lancer `codebox version`.
2. Vérifier le diagnostic sans effet de bord : définir `$MSB_HOME` vers un
   répertoire temporaire vide et retirer `MSB_PATH` et
   `MSB_LIBKRUNFW_PATH`, puis lancer
   `codebox doctor --microsandbox`. La commande doit échouer en indiquant
   que le runtime manque, sans créer de fichier. Répéter après installation
   et confirmer que le runtime est diagnostiqué. Lancer aussi
   `codebox setup doctor` et `codebox setup doctor --json`; les deux
   rapports doivent être cohérents et ne contenir aucune valeur secrète.
   Conserver localement le JSON comme pièce d'évidence; le relire et le
   redacter avant de le joindre à un ticket ou à un compte rendu.
3. Les workflows GitHub Actions fournissent les preuves de build et des tests
   unitaires par architecture. Ne lancer `go -C apps/codebox test ./...` que depuis un
   checkout contributeur avec Go et les outils C requis; son absence sur un
   poste d'atelier ne bloque pas le test du binaire installé.
4. Avec un projet Git jetable, tester le parcours utilisateur vide :
   `codebox init`, puis lancer `codebox --microsandbox --isolation full`.
   Après démarrage, exécuter `codebox check --isolation full`. Vérifier que
   le TUI tourne dans l'invité et que `/workspace` est invité, pas un montage
   du checkout hôte.
5. Créer un fichier-canari non secret dans le checkout hôte et un fichier
   `.env` factice contenant `ALBERT_API_KEY=host-canary-not-a-secret`. Après
   synchronisation ou actualisation, vérifier depuis l'invité que le canari
   filtré n'est pas lisible. Ne pas utiliser une vraie valeur de secret.
6. Modifier un fichier dans l'invité, lancer `codebox workspace export` pour
   produire un diff, revoir ce diff, puis appliquer uniquement les changements
   retenus. Confirmer qu'aucune écriture directe n'a touché le checkout avant
   l'application explicite.
7. Tester `codebox stop`, le redémarrage non destructif et la reconnexion.
   Confirmer que l'état annoncé comme persistant l'est, sans recréation
   implicite.
8. Répéter dans un projet existant avec des fichiers de configuration et du
   texte utilisateur préexistants. Pour le parcours importé, utiliser
   `codebox import albert-code` d'abord en aperçu, puis appliquer seulement
   après revue. Vérifier que l'import répété est stable et que les réglages
   inconnus/non gérés restent intacts.
9. Cloner un dépôt d'équipe jetable qui contient une configuration
   codebox versionnée. Vérifier `codebox check`, un `codebox update`
   sans changement attendu, puis le démarrage et l'export de changements dans
   une branche de test. Ne pas utiliser un dépôt contenant des secrets ou du
   contenu de production.
10. Sur macOS arm64 uniquement, exécuter les parcours réseau de
    `apps/codebox/tests/integration/credentials/README.md` dans le laboratoire dédié et avec
    une clé de test révocable. Distinguer un échec de préparation du laboratoire
    d'un échec d'assertion. Le laboratoire documenté dépend des interfaces et
    commandes macOS : ne pas présenter ce résultat comme preuve de transport
    sur Linux ou Windows.
11. Pour les runtimes explicitement revendiqués, exécuter leurs diagnostics et
    un cycle start/stop/reprise séparément : Tart sur macOS arm64 ; agent-vm sur
    macOS/Linux avec Lima. Aucun résultat de ces runtimes ne qualifie
    Microsandbox.

## Protocoles par plateforme

Exécuter les étapes communes 1 à 11 sur chaque hôte, puis les contrôles
spécifiques ci-dessous. Une cible sans runtime disponible ou avec un
prérequis d'hyperviseur absent est « non qualifiée », pas un échec de
codebox : noter l'écart dans la matrice.

### macOS arm64

Prérequis : Apple Silicon, Hypervisor.framework, OpenCode, codebox.

1. Microsandbox : installer par `codebox start --microsandbox`, vérifier
   `codebox doctor --microsandbox`, lancer le parcours en isolation `full`,
   puis `codebox stop`.
2. Tart : installer Tart explicitement, vérifier `codebox doctor --tart`,
   lancer `codebox start --tart --isolation backend`, vérifier la connexion
   du TUI hôte et l'accès au endpoint local, puis `codebox stop --tart`.
3. agent-vm : installer Lima, vérifier `codebox doctor --agent-vm`, lancer
   `codebox start --agent-vm --isolation backend`, vérifier que le template
   est construit une seule fois et que le redémarrage conserve l'état, puis
   `codebox stop --agent-vm`.
4. Noter si l'hôte est une machine de l'utilisateur, une machine CI dédiée,
   ou un Mac distant géré, car la confiance n'est pas équivalente.

### Linux amd64

Prérequis : `/dev/kvm` ouvert par l'utilisateur, OpenCode, codebox.

1. Vérifier `test -r /dev/kvm -a -w /dev/kvm` ; le résultat et la commande
   doivent apparaître dans le rapport.
2. Microsandbox : installer par `codebox start --microsandbox`, vérifier
   `codebox doctor --microsandbox`, lancer le parcours en isolation `full`,
   puis `codebox stop`.
3. agent-vm : installer Lima explicitement, vérifier `codebox doctor
   --agent-vm`, lancer `codebox start --agent-vm --isolation backend`, puis
   `codebox stop --agent-vm`. Ne pas déduire le succès Microsandbox du
   succès Lima.
4. Si l'hôte est une VM cloud, ne qualifier Microsandbox que lorsque KVM est
   réellement disponible dans l'invité ; sinon la preuve est invalide.

### Linux arm64

Prérequis et parcours identiques à Linux amd64, mais l'architecture native
doit être confirmée (`uname -m` = `aarch64`). Ne pas accepter un test amd64
émulation comme preuve arm64.

### Windows amd64

Prérequis : Windows 10 ou 11 x64, Windows Hypervisor Platform activée,
OpenCode, codebox.

1. Vérifier la fonctionnalité WHP dans les fonctionnalités Windows, puis
   redémarrer si elle vient d'être activée.
2. Exécuter le protocole commun dans PowerShell, y compris le parcours
   d'installation avec `install.ps1`.
3. Microsandbox : installer par `codebox start --microsandbox`, vérifier
   `codebox doctor --microsandbox`, lancer le parcours en isolation `full`,
   puis `codebox stop`.
4. Ne pas remplacer WHP par WSL2 ou Hyper-V embarqué sans preuve : le backend
   annoncé est WHP.

### Windows arm64

Prérequis : Windows 10 ou 11 arm64, WHP, OpenCode, codebox.

1. Confirmer l'architecture native (`$env:PROCESSOR_ARCHITECTURE` = `ARM64`).
2. Exécuter le protocole commun dans PowerShell, y compris `install.ps1`.
3. Microsandbox : installer par `codebox start --microsandbox`, vérifier
   `codebox doctor --microsandbox`, lancer le parcours en isolation `full`,
   puis `codebox stop`.
4. Les runtimes Tart et agent-vm ne sont pas qualifiés sur Windows arm64 ;
   ne pas marquer cette combinaison comme supportée.

## Matrice de preuve

Compléter une ligne par combinaison réellement testée. « Non testé » et
« bloqué par l'hôte » ne valent pas « réussi ».

| OS / architecture | Hôte / contexte | Runtime / backend natif | Version CLI / runtime | `setup doctor` | Doctor runtime | Parcours | Résultat / lien vers preuve |
|---|---|---|---|---|---|---|---|
| macOS arm64 | | Microsandbox / Apple Virtualization | | | | | |
| macOS arm64 | | Tart / Apple Virtualization | | | | | |
| macOS arm64 | | agent-vm / Lima | | | | | |
| Linux amd64 | | Microsandbox / KVM | | | | | |
| Linux arm64 | | Microsandbox / KVM | | | | | |
| Linux amd64 ou arm64 | | agent-vm / Lima | | | | | |
| Windows amd64 | | Microsandbox / WHP | | | | | |
| Windows arm64 | | Microsandbox / WHP | | | | | |

Pour chaque résultat, conserver les commandes, le code de sortie et les
assertions pertinentes. Masquer les clés, mots de passe, jetons, identifiants
de comptes et chemins personnels avant de partager une sortie. Ne pas joindre
les journaux bruts d'un laboratoire TLS : ils peuvent contenir les valeurs de
test envoyées aux serveurs.

## Parcours secrets

Ces parcours se qualifient **sans microVM** pour tout ce qui touche le magasin
et les invites : le magasin d'identifiants vit sur l'hôte. Ce qui concerne la
livraison exige un invité démarré, donc une plateforme qualifiée.

Préparer un projet jetable avec deux fichiers dotenv (un à la racine, un dans
un sous-répertoire) et une valeur de test révocable. **Ne jamais** utiliser une
valeur réelle : les captures et les sorties conservées ne doivent contenir
aucun secret.

1. **Import.** `secrets import --profile development --file .env` puis
   `--apply`. Attendu : l'aperçu masque les valeurs, signale les clés répétées,
   n'écrit rien ; `--apply` demande confirmation dans un terminal ; un script
   sans `--yes` ne doit pas écrire. Vérifier que les fichiers sources sont
   inchangés (comparer leur empreinte avant/après).
2. **Approbation proxy.** `secrets bindings approve … --destination <hôte>`.
   Attendu : l'approbation est enregistrée localement, jamais dans le dépôt ;
   `bindings list` la montre ; l'invité ne reçoit qu'un marqueur.
3. **Approbation lisible dans l'invité.** `… --delivery guest`. Attendu :
   l'avertissement est affiché ; le fichier du composant contient la valeur
   réelle ; il est en `0600` ; `bindings list` distingue les deux modes.
   Tester les deux issues, car elles ne portent pas sur les mêmes valeurs :
   - **doivent être livrées** et relues à l'identique — une apostrophe, deux
     antislashs consécutifs, une valeur finissant par un antislash, un chemin
     Windows (`C:\Users\…\`). Ces formes sont portées par l'encodage non quoté
     et ne doivent **pas** être refusées ;
   - **doivent être refusées**, en nommant la variable et la raison — un `$`,
     un retour chariot, une valeur en UTF-8 invalide, et une valeur commençant
     par une apostrophe. Ce sont les seules classes qu'aucune des deux formes
     ne porte dans le cas simple.
4. **Profils.** `secrets profile <nom>`, puis `--clear` alors que des liaisons
   restent approuvées : le refus est attendu. Une liaison d'un autre profil que
   l'actif doit faire échouer le démarrage, pas être ignorée.
5. **Rotation.** Remplacer la valeur dans le magasin, redémarrer, vérifier que
   la nouvelle valeur est livrée et que l'ancienne n'apparaît plus.
6. **Révocation.** `secrets bindings revoke`. Le message doit décrire le mode
   réellement approuvé ; après application, la valeur ne doit plus être lisible
   dans l'invité. Sur le mode proxy, l'invité garde un marqueur inerte.
7. **Magasin verrouillé.** Rendre le magasin indisponible (verrouiller la
   session, ou retirer l'accès au service) puis démarrer : le démarrage doit
   **échouer** avec un message explicite, jamais poursuivre en retirant la
   liaison — une liaison omise deviendrait l'état désiré et désinscrirait une
   inscription active.

### Terminal et annulation

À exécuter sur les invites d'import et d'approbation :

| Situation | Attendu |
| --- | --- |
| Terminal étroit (~40 colonnes) | Aucun débordement ; le libellé et la valeur attendue restent lisibles |
| Terminal large (~200 colonnes) | Aucun étirement ni alignement cassé |
| Entrée/sortie redirigées (non-TTY) | Repli sur un mode ligne ; aucune invite interactive ; `--yes` requis pour écrire |
| Ctrl-C à chaque invite | Rien n'est enregistré : ni approbation partielle, ni profil changé |
| Interruption au milieu d'une application | Une nouvelle exécution converge vers le même état (idempotence) |

Conserver une capture de l'aperçu d'import et de l'avertissement de livraison
lisible dans l'invité pour la revue UX, en masquant les valeurs.

### Matrice des magasins d'identifiants

À qualifier séparément du runtime, sur une session où le service répond.

| Plateforme | Magasin | `put` / `get` / `remove` | Valeur absente de `argv` | Verrouillage géré |
|---|---|---|---|---|
| macOS | Trousseau | | | |
| Linux | Secret Service | | | |
| Windows | Credential Manager | | | |

Un magasin absent ou non fonctionnel donne « non qualifié », jamais « réussi » :
les tests du magasin réel sont conditionnés à sa disponibilité et s'ignorent
ailleurs, y compris dans les conteneurs d'intégration continue.

### Credential Manager Windows : protocole avec le binaire installé

Ce protocole qualifie le vrai magasin Windows sans toolchain Go : les
commandes `codebox auth` appellent le même chemin que les tests
`TestRealStore…` (PowerShell, P/Invoke `advapi32` : `CredWriteW`,
`CredReadW`, `CredDeleteW` sur la cible `codebox/<type>`). Il répond à
l'incident #293 : sur Windows Server 2025, une lecture a échoué
(`credential not found`) immédiatement après une écriture acceptée, une
seule fois sur deux exécutions. La cause n'est pas établie ; ce protocole
est l'instrument pour la trancher sur des postes Windows 10/11 réels.

Prérequis : le binaire `codebox` installé, PowerShell, un poste Windows 10
ou 11 (amd64 ou arm64 natif). Aucune microVM n'est nécessaire : le magasin
vit sur l'hôte.

**Valeur de test uniquement.** Utiliser un marqueur synthétique, par
exemple `qa-wincred-marker-1`. Ne jamais utiliser une vraie clé : les
sorties conservées ne doivent contenir aucun secret. Si une vraie clé
Albert est déjà stockée, ne pas lancer ce protocole sur ce poste : il
écrase et supprime l'entrée `albert`.

1. Noter avant de commencer : édition et version de Windows, architecture
   (`$env:PROCESSOR_ARCHITECTURE`), type de session (console interactive,
   RDP ou SSH), session élevée ou non, version du CLI (`codebox version`).
2. État initial : `codebox auth status`. Noter la ligne `Native store:` et
   l'état du magasin. Un état `denied` ou `unavailable` arrête le
   protocole : c'est un échec de préparation, pas un résultat sur le
   cycle écriture/lecture.
3. Écriture :
   `Read-Host "Marqueur de test" -AsSecureString | ConvertFrom-SecureString -AsPlainText | codebox auth add albert --stdin`.
   Saisir `qa-wincred-marker-1` à l'invite masquée. La valeur n'apparaît
   ni à l'écran, ni dans la ligne de commande, ni dans l'historique
   PowerShell. `ConvertFrom-SecureString -AsPlainText` (PowerShell 7+)
   rend le texte au pipe ; sur Windows PowerShell 5.1, utiliser
   `[System.Net.NetworkCredential]::new("", $m).Password` après
   `$m = Read-Host "Marqueur de test" -AsSecureString`. Attendu : message
   `Stored credential "albert" in the credential-manager store`, code de
   sortie 0. Noter l'horodatage.
4. Relecture immédiate : `codebox auth status`. Attendu : la ligne
   `albert: stored` sous `Native store:`. Noter l'horodatage et le délai
   écoulé depuis l'écriture.
5. Suppression : `codebox auth remove albert`. Attendu : `Removed
   credential "albert" from the credential-manager store`. Puis
   `codebox auth status` doit montrer `albert: not stored`.
6. Répéter les étapes 3 à 5 une seconde fois, consécutivement, en
   saisissant `qa-wincred-marker-2` à l'invite masquée. Les deux cycles
   complets doivent passer dans la même session.
7. Valeur absente de `argv` et de l'historique : la commande de l'étape 3
   passe la valeur par stdin, jamais en argument de `codebox`. Comme la
   saisie est masquée, le marqueur ne figure pas non plus dans la ligne de
   commande. Vérifier que l'historique PowerShell
   (`Get-History` ou le fichier PSReadLine) ne contient que la commande
   avec `Read-Host`, sans le marqueur, et que les sorties conservées ne
   contiennent aucune valeur. L'historique du shell prouve ce qui a été
   tapé, pas ce que le processus a reçu en argument : la garantie `argv`
   vient du chemin stdin, pas de l'historique.

**Signatures d'échec à distinguer** (les noter telles quelles, avec
l'horodatage) :

- `albert: not stored` immédiatement après une écriture acceptée : c'est
  la signature #293. La lecture a répondu « entrée absente » juste après
  une écriture acceptée ; l'adaptateur ne distingue pas ce cas d'une
  entrée réellement absente (le script mappe ERROR_NOT_FOUND, code Win32
  1168, vers cette réponse). Dans les tests Go du spike, la même
  condition apparaissait sous la forme `Get: credential not found`.
  Noter le délai entre les deux commandes et le type de session.
- `denied: exit 2` : le magasin refuse l'opération. C'est un refus
  d'accès, pas une entrée absente ; ne pas le confondre avec la signature
  ci-dessus.
- `unreadable (...)` : toute autre erreur de lecture. Conserver le
  message complet.
- Un test ignoré ou une sonde sans réponse n'est jamais un passage.

**Règle de qualification** : la ligne Windows de la matrice ci-dessus
n'est marquée qualifiée qu'après deux cycles complets consécutifs
passants, sur une édition Windows 10/11, avec les horodatages et le type
de session consignés. Un résultat sur Windows Server ne qualifie pas les
cibles Windows 10/11 ; il est consigné séparément. Reporter le résultat
réel dans `docs/plateformes.md` et joindre les sorties (sans valeur) au
ticket de qualification.

## Interprétation

- CI verte prouve les cas exécutés par ses jobs, pas la disponibilité d'un
  hyperviseur ni le fonctionnement d'une microVM.
- Un échec avant le démarrage du runtime est un échec de préparation, pas une
  preuve sur le comportement invité.
- Une combinaison sans preuve reste « non qualifiée » ; ne pas l'annoncer
  comme supportée sur la seule base d'une compilation native.
- Toute violation du filtre de transfert, tout montage hôte présent dans un
  chemin Microsandbox scellé, toute mutation par `doctor` ou toute fuite de
  secret bloque la qualification jusqu'à correction et nouvelle exécution.
