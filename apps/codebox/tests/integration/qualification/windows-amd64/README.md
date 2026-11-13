# Spike Windows amd64 — 2026-10-28

Ce dossier conserve les preuves du spike demandé dans #77, pas une
qualification complète de Windows. Il ne contient aucune clé réelle : le
projet et les valeurs de test sont synthétiques.

## Hôte et révision

- Source : `256e4233845aae324e924e7be6c73a3ef11c663b`, fusion de #289 (#275).
- Hôte : Scaleway Elastic Metal `EM-B320E-NVME` dédié, AMD EPYC 7232P,
  8 coeurs, `pl-waw-3`.
- OS : Windows Server 2025 Standard 24H2, amd64, WHP activé puis redémarrage.
- Runtime : Microsandbox 0.7.11, archive SHA-256 vérifiée avant extraction.
- Compilation : Go 1.26.10, LLVM-MinGW 20261020, CGO activé.

`host-state.txt` enregistre l'architecture, l'état WHP et le hyperviseur.
Le serveur est une édition différente des cibles Windows 10/11 du protocole
actuel : le résultat ne vaut donc pas acceptation complète de #77.

## Résultats

| Vérification | Résultat | Preuve |
| --- | --- | --- |
| Compilation native de just-code | Réussie | `native-build.txt` |
| Runtime WHP et `just-code doctor --microsandbox` | Prêts | `msb-direct.txt`, `just-code-doctor.txt` |
| Démarrage d'une microVM Alpine via WHP | Réussi ; invité `x86_64` | `microvm-boot.txt` |
| Workspace scellé et filtre dotenv | Réussi ; `.env` factice absent du guest | `just-code-full.txt`, `microvm-boot.txt` |
| Arrêt/redémarrage avec conservation du workspace | Réussi | `microvm-boot.txt` |
| TUI OpenCode en isolation complète | Bloqué par #292 | `just-code-full.txt`, `just-code-deepseek.txt` |

Le diagnostic avec `$MSB_HOME` vide échoue sans créer de fichier ; avec le
runtime explicitement fourni, il réussit. Le guest exécute OpenCode 1.18.34.
La sortie observée lors du contrôle final du guest est :

```text
DIRECT_BOOT_EXIT=0
MICROVM_OK
x86_64
GUEST_OK
/workspace
DOTENV_FILTER_EXIT=0
CANARY_EXIT=0
PERSISTED_CANARY_EXIT=0
LIFECYCLE_EXIT=0
```

Ces assertions sont aussi reproduites dans `microvm-boot.assertions.txt` :
PowerShell a capturé les codes de sortie dans son journal, tandis que les
sorties guest sont restées dans le flux SSH.

Le lancement complet atteint la microVM et la préparation du workspace, mais
OpenCode refuse `provider.albert.models.qwen3-coder.limit.output`. La sélection
DeepSeek ne change pas l'erreur, car la définition Qwen reste dans la
configuration. Ce défaut est distinct de WHP et n'est attribué ni à Windows
ni à la valeur d'identifiant factice.

## Credential Manager

Les tests du vrai magasin sont sous
`tests/integration/credentials/evidence/` :

- `windows-amd64-ssh-skip.txt` : les deux tests sont ignorés en SSH ; ce n'est
  pas une preuve de réussite.
- `windows-amd64-interactive-run-1.txt` : `Get` ne retrouve pas la valeur après
  un `Put` accepté ; le test d'absence passe.
- `windows-amd64-interactive-run-2.txt` : exécution séparée, les deux tests
  passent avec les marqueurs synthétiques du dépôt.

Le service réel a été exercé, mais l'intermittence empêche de le marquer comme
qualifié. La suite est suivie dans #293 ; Windows arm64 est suivi dans #291.

## Limites de portée

Le spike ne couvre ni l'installation/mise à niveau sur Windows 10/11, ni les
parcours interactifs OpenCode et migration, ni Windows arm64. Il ne ferme pas
#77 et ne prouve pas la parité multiplateforme.
