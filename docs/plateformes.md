# Plateformes prises en charge et niveau de qualification

Ce document distingue deux choses qu'il est tentant de confondre :

- **pris en charge** : le runtime existe pour cette plateforme, codebox sait le
  télécharger et le vérifier, et la compilation comme les tests passent en
  intégration continue ;
- **qualifié** : une microVM a réellement démarré sur cette plateforme, avec une
  preuve exécutée et conservée dans le dépôt.

Une plateforme peut être prise en charge sans être qualifiée. C'est le cas de
toutes sauf macOS arm64, Linux amd64 et Linux arm64 aujourd'hui. Aucun résultat de compilation ne remplace
un démarrage réel : les runners hébergés n'ont pas de virtualisation imbriquée,
et le fichier `.github/workflows/ci.yml` le dit à l'endroit où il définit ces
tâches.

## Runtime Microsandbox

| Plateforme | Artefact du runtime | Compilé et testé en CI | MicroVM réellement démarrée |
| --- | --- | --- | --- |
| macOS arm64 (`darwin/arm64`) | oui | oui | **oui** |
| Linux x86-64 (`linux/amd64`) | oui | oui | **oui** |
| Linux arm64 (`linux/arm64`) | oui | oui | **oui** |
| Windows x86-64 (`windows/amd64`) | oui | oui | **spike réussi** |
| Windows arm64 (`windows/arm64`) | oui | oui | non |
| macOS Intel (`darwin/amd64`) | **non** | — | — |

macOS Intel n'est pas une plateforme prise en charge, et ne doit pas être
annoncée comme telle : la version épinglée du runtime ne publie aucun artefact
`darwin-x86_64`, ni binaire `msb`, ni bibliothèque FFI `darwin-amd64`. codebox
refuse donc explicitement cette combinaison plutôt que d'échouer plus tard, et
un test le vérifie (`msbRuntimeArtifactFor`).

Les cinq combinaisons prises en charge ont une empreinte SHA256 épinglée dans
`internal/codebox/msb_runtime_install.go` : le runtime téléchargé est vérifié
avant d'être extrait, et une version publiée différente n'est pas adoptée
silencieusement.

Le 2026-10-28, un spike sur un hôte dédié Scaleway Elastic Metal sous Windows
Server 2025 amd64 a démarré une microVM Alpine réelle via WHP avec le runtime
0.7.11 vérifié. Le parcours codebox a aussi créé le workspace invité scellé,
exclu le fichier dotenv factice et conservé les fichiers après arrêt/redémarrage.
Ce spike n'est **pas** une qualification complète de #77 : l'édition testée
diffère des cibles Windows 10/11 du protocole actuel, et le lancement OpenCode
est bloqué par #292. Voir `tests/integration/qualification/windows-amd64/`.
Windows arm64 reste non testé (#291).

## Magasins d'identifiants natifs

Le magasin est une question distincte du runtime : il vit sur l'hôte et ne
dépend pas de la virtualisation. C'est pourquoi sa matrice se qualifie
séparément.

| Plateforme | Adaptateur | Contrat de l'adaptateur testé | Magasin réel exercé |
| --- | --- | --- | --- |
| macOS | Trousseau (`security`) | oui | **oui** |
| Linux | Secret Service (`secret-tool`) | oui | **oui** |
| Windows | Credential Manager (PowerShell, P/Invoke `advapi32`) | oui | non (résultats mixtes) |

Le vrai magasin Windows a été exercé le 2026-10-28 dans une session PowerShell
interactive sur Windows Server 2025. Une exécution a réussi `put` / `get` /
`remove`, la rotation et l'absence, mais une autre a échoué à la lecture
immédiate après écriture ; les sessions SSH ont ignoré le test après refus de
la sonde. Le service est donc réellement atteint, mais pas qualifié : la cause
de cette intermittence reste à établir dans #293. Les trois transcripts sont
sous `tests/integration/credentials/evidence/`.

Le Trousseau a été qualifié sur macOS arm64 le 2026-10-16 (hôte Scaleway
Mac mini M4, trousseau dédiée déverrouillée — sur un hôte headless, le
trousseau `login` n'est pas déverrouillable) : les transcripts
`darwin-arm64-run-1.txt` et `darwin-arm64-run-2.txt` sous
`tests/integration/credentials/evidence/` montrent `put` / `get` / `remove`,
rotation et retrait d'élément absent, exécutés deux fois de suite contre la
révision qualifiée. Le magasin Linux est qualifié depuis le même jour, sur
amd64 comme sur arm64 : transcripts `--- PASS` conservés
(`linux-amd64-run-1.txt`, `linux-amd64-run-2.txt`,
`linux-arm64-run-1.txt`, `linux-arm64-run-2.txt`), obtenus dans une session
D-Bus avec `gnome-keyring-daemon` déverrouillé. Hors session, le test
`TestRealStorePutGetRemoveRoundTrip` ne s'ignore pas : il **échoue** sur un
hôte Linux sans tête où la collection est verrouillée
(`linux-amd64-nosession.txt`, `linux-arm64-nosession.txt`), parce que la
sonde de disponibilité est en lecture seule et déclare le magasin
« accessible » alors que les écritures échouent. Un transcript qui échoue
ainsi n'est pas une régression du produit mais il n'est pas une preuve non
plus : seule une exécution passante sur un magasin réellement utilisable
qualifie la ligne. Sur un poste avec une session ouverte, la commande de
preuve reste `go test ./internal/codebox/ -run TestRealStore -v` ; un test
ignoré n'est pas une preuve.

La qualification microVM de Linux arm64 a été obtenue sur un hôte KVM réel
(Scaleway Mac mini M2 sous Asahi Fedora 42, noyau arm64 à pages de 16 K) ;
les preuves sont sous `tests/integration/qualification/linux-arm64/`. Les
Instances Scaleway arm64 classiques n'exposent pas de virtualisation
emboîtée et ne peuvent donc pas servir d'hôtes de qualification pour la
microVM.

Le contrat testé partout est celui qui compte pour la confidentialité : la
valeur ne passe jamais par argv — elle est écrite sur l'entrée standard de
`secret-tool` sous Linux, et sur celle de PowerShell sous Windows, où le script
est transmis encodé pour qu'une liste de processus ne montre rien du secret —
et les échecs du magasin sont traduits en états explicites plutôt qu'en silence.
Sous Windows, `cmdkey` n'est pas utilisé : il ne sait pas relire une valeur
générique. Les
tests du magasin réel sont conditionnés à sa disponibilité : ils s'exécutent sur
une machine où le service répond, et sont ignorés ailleurs. Un conteneur
d'intégration continue a bien `secret-tool`, mais aucune session D-Bus ne
répond ; c'est pourquoi la colonne « magasin réel exercé » n'est pas la même que
la colonne « testé ».

Le magasin de repli, en clair et lisible par le seul propriétaire, n'est jamais
sélectionné implicitement : il exige un consentement explicite (`--fallback`),
et son état est rappelé à chaque affichage.

## Où vivent les preuves

| Preuve | Emplacement |
| --- | --- |
| Qualification complète Linux amd64 (parcours commun, secrets, terminal, installateur) | `tests/integration/qualification/linux-amd64/` |
| Qualification complète macOS arm64 (parcours commun, secrets, terminal, agent-vm, laboratoire d'identifiants) | `tests/integration/qualification/darwin-arm64/` |
| Qualification complète Linux arm64 sur hôte KVM (parcours commun, secrets, terminal, installateur) | `tests/integration/qualification/linux-arm64/` |
| Spike Windows amd64 sur Windows Server dédié (microVM, workspace, limites) | `tests/integration/qualification/windows-amd64/` |
| Qualification native du transport proxy et des marqueurs (phase 0) | `tests/integration/project-secrets/native/evidence/` |
| Qualification native de bout en bout des fichiers dotenv gérés (phase 3) | `tests/integration/project-secrets/native-phase3/evidence/` |
| Magasin d'identifiants natif exercé (macOS, Linux, Windows) | `tests/integration/credentials/evidence/` |
| Matrice des chargeurs dotenv (Node, Python) | `tests/integration/project-secrets/` |
| Protocole de qualification manuelle par hôte | `docs/qualification-beta.md` |

Chaque transcript conservé porte une ligne `META` nommant la révision du
harnais et le commit qualifié : une preuve qui ne dit pas ce qu'elle a exécuté
n'en est pas une.

## Ce qui n'est pas promis

- Démarrer une microVM sur Windows amd64 : le spike Windows Server 2025 ci-dessus
  est réussi, mais la qualification complète Windows 10/11 et Windows arm64
  reste à obtenir.
- Exercer le magasin natif Windows : le service a été atteint, mais sa fiabilité
  reste à démontrer dans #293.
- Lancer le parcours OpenCode complet : bloqué sur toutes les plateformes par
  le défaut de configuration suivi dans #292 tant que cette issue reste ouverte.
- macOS Intel, sous aucune forme.
- Une plateforme non listée : le comportement est de refuser, pas de dégrader.
