# Choix de conception

- **Microsandbox/full par défaut.** `codebox` sans commande lance le TUI dans l'invité. Aucun serveur OpenCode ni port 4096 n'est publié en mode full ; le mode backend explicite utilise `opencode serve` sur 4096. Les previews des ports 3000-3010 restent accessibles sur `localhost`.
- **Runtimes alternatifs explicites.** Tart et agent-vm restent disponibles via `--tart`, `--agent-vm` ou `RUNTIME` et gardent leurs propres contraintes de workspace et de transport des identifiants.
- **VM macOS avec Tart pour Xcode/iOS.** Tart permet d'exécuter l'agent OpenCode directement dans un système macOS invité, donnant accès aux outils de compilation Xcode (`xcodebuild`, `swift`, simulateurs). Par défaut, l'image `ghcr.io/cirruslabs/macos-tahoe-base:latest` est clonée dans une VM locale nommée `opencode-tahoe-base-latest` (surchargeable via `TART_IMAGE`). L'utilisateur ou le développeur peut ensuite y installer les outils Xcode nécessaires. La VM étant une machine invitée macOS sur le réseau NAT, le TUI et les previews sont joints par son adresse (`tart ip opencode-tahoe-base-latest`) plutôt que par `localhost`. `ALBERT_API_KEY` est transmise sur l'entrée standard du processus de bootstrap, jamais dans la liste des arguments ; seul le binaire du CLI (copie dédiée en lecture seule) est partagé avec la VM, pas le dépôt ni `.env`.
- **MicroVM nommée et persistante.** Avec Microsandbox et Tart, `codebox stop` conserve l'état inscriptible de la VM et les relances ultérieures évitent de repartir de zéro. `codebox restart` arrête puis relance la même VM ; `codebox recreate` la reconstruit explicitement.
- **Pas de démon ni de runtime conteneur.** La microVM démarre à la demande depuis l'image OCI officielle `ghcr.io/anomalyco/opencode:latest`.
- **Workspace Microsandbox scellé.** `/workspace` est un volume possédé par l'invité ; le checkout hôte n'est jamais monté. Les fichiers traversent le filtre par défaut-deny et le retour des changements se fait par export revu. Tart et agent-vm continuent de monter le workspace hôte.
- **Configuration OpenCode générée.** La configuration du provider Albert est passée inline via `OPENCODE_CONFIG_CONTENT` par le SDK ; le workspace n'est pas utilisé comme source de configuration hôte montée.
- **Permissions permissives dans le sandbox.** Le runtime sélectionné est la frontière de confinement : `edit`, `bash` et `external_directory` sont autorisés à l'intérieur.
- **Secrets Microsandbox.** La vraie valeur reste sur l'hôte : seule une valeur de substitution entre dans la microVM et le proxy réseau ne la remplace que pour `albert.api.etalab.gouv.fr`. Pour Tart et agent-vm, la clé est lisible dans l'invité et un avertissement/accord explicite s'applique.
- **Filtre de transfert Microsandbox.** Les fichiers `.env`, les liens symboliques et les fichiers signalés par le détecteur de secrets embarqué (gitleaks épinglé, fourni par codebox) sont exclus par défaut à chaque transfert et rafraîchissement ; une ré-inclusion se fait fichier par fichier. Tart et agent-vm montent le workspace hôte et conservent le scan bloquant au démarrage.

## Documents de décision

- [D-001 : contrat de configuration et de découverte d'OpenCode](decisions/2026-09-23-opencode-configuration-contract.md)
- [D-002 : transport des identifiants Microsandbox (substitution proxy)](decisions/2026-09-24-microsandbox-credential-transport.md)
- [D-003 : capacités invité Microsandbox et comportement PTY](decisions/2026-09-24-microsandbox-guest-capabilities.md)
- [D-004 : espace de travail invité scellé (clone/instantané filtré, retour revu)](decisions/2026-09-24-sealed-workspace.md)
- [Refonte de l'UI wizard avec huh](decisions/2026-10-06-huh-wizard-ui.md)
