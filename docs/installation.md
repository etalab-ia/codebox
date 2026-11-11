# Installation

## Binaire précompilé (recommandé)

Chaque [release](https://github.com/etalab-ia/codebox/releases) publie des binaires pour `darwin-arm64`, `linux-arm64`, `linux-x64`, `windows-x64.exe` et `windows-arm64.exe`, avec un fichier `SHA256SUMS`. macOS Intel n'est pas pris en charge : le SDK Microsandbox ne fournit pas de bibliothèque FFI pour `darwin/amd64`.

Sur macOS ou Linux :

```sh
curl -fsSL https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.sh | sh
```

Dans PowerShell sous Windows :

```powershell
irm https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.ps1 | iex
```

L'installateur choisit une seule release stable, télécharge le binaire et `SHA256SUMS` depuis cette même release, puis compare l'empreinte avant de remplacer l'installation. Le binaire est installé sans privilèges élevés sous `~/.local/bin` (macOS/Linux) ou `$env:LOCALAPPDATA\Programs\codebox` (Windows). Une mise à niveau conserve le binaire précédent sous `codebox.previous` (macOS/Linux) ou `codebox.previous.exe` (Windows). Sous Windows, ferme les sessions `codebox` en cours avant la mise à niveau. Si `codebox setup` échoue, restaure le binaire précédent dans le même répertoire ; sous Windows, par exemple : `Copy-Item -LiteralPath "$env:LOCALAPPDATA\Programs\codebox\codebox.previous.exe" -Destination "$env:LOCALAPPDATA\Programs\codebox\codebox.exe" -Force`. L'assistant `codebox setup` est lancé après l'installation.

Pour examiner le script avant de l'exécuter, télécharge-le puis lance-le :

```sh
curl -fsSL https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.sh -o install-codebox.sh
sh install-codebox.sh
```

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.ps1 -OutFile install-codebox.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\install-codebox.ps1
```

La forme Windows en deux étapes passe `-ExecutionPolicy Bypass` uniquement pour le processus PowerShell lancé par cette commande, car la stratégie `Restricted` par défaut bloque les scripts fichiers ; aucune stratégie permanente n'est changée. La forme pipe (`irm ... | iex`) évalue le script en mémoire et n'est pas soumise à la stratégie d'exécution.

Si le répertoire d'installation n'est pas déjà dans `PATH`, l'installateur prévient que la commande ne serait pas trouvée dans un nouveau terminal, puis demande l'autorisation d'ajouter la ligne correspondante au fichier réellement lu par ce terminal :

- zsh : `~/.zshenv` ;
- bash sous macOS : `~/.bash_profile` s'il existe, sinon `~/.profile` (un `~/.bash_profile` n'est jamais créé de zéro, car il masquerait un `~/.profile` existant) ;
- bash sous Linux : `~/.bashrc`, le seul fichier lu par un terminal qui ouvre un shell non connecté ;
- `sh`, `dash`, `ksh`, `ash` : `~/.profile` ;
- fish, nushell et shells inconnus : aucune modification n'est proposée, car un `export` POSIX n'y aurait pas d'effet ; l'installateur affiche la ligne à ajouter au fichier de démarrage de ce shell.

Il attend une réponse ; appuyer sur Entrée accepte, répondre `n` refuse. La modification n'est jamais faite d'office, et une ligne déjà présente n'est pas ajoutée une seconde fois. Une lecture impossible (Ctrl-D) est traitée comme un refus, pas comme une acceptation. Lorsque aucun terminal n'est disponible (script non interactif, `curl … | sh` sans terminal de contrôle), l'installateur ne modifie aucun fichier de profil et se contente d'afficher la ligne à exécuter dans la session courante. Le résultat de l'installation est annoncé avant le lancement de l'assistant, et un assistant interrompu ou en échec indique la commande de reprise, donnée en chemin absolu (`"$HOME/.local/bin/codebox" setup`) car le terminal courant n'a pas encore le nouveau `PATH`.

La somme SHA-256 détecte une altération ou un mélange d'assets, mais elle est téléchargée depuis la même release GitHub que le binaire. Les releases des binaires `codebox` ne publient pas d'attestation vérifiée de provenance : cette vérification ne prouve donc pas l'identité de l'éditeur. Les scripts d'installation ne sont pas signés ni attestés non plus ; leur provenance doit être évaluée séparément, par exemple avec la variante téléchargeable ci-dessus.

Les binaires macOS ne sont ni signés ni notariés, et portent une signature ad-hoc (requise pour exécuter un binaire non signé sur Apple Silicon). `curl` ne pose pas l'attribut `com.apple.quarantine`, donc Gatekeeper ne bloque pas ce chemin d'installation ; un téléchargement via navigateur reste à débloquer avec `xattr -d com.apple.quarantine <binaire>`. La notarisation complète exigerait une adhésion Apple Developer et un certificat Developer ID.

## Dépendances de l'hôte

- Une **clé d'au moins une plateforme d'inférence** : Albert API (par défaut), Mistral AI ou une plateforme compatible OpenAI, dans l'environnement ou dans le magasin d'identifiants (voir [Identifiants et secrets](credentials.md))
- OpenCode CLI sur l'hôte (`npm uninstall -g opencode-ai && npm install -g @opencode/cli@2.2.5`) **uniquement en isolation partielle** : en isolation complète (le défaut), codebox installe OpenCode dans l'invité et ne le vérifie pas sur l'hôte.
- L'un des runtimes disponibles :
  - [Microsandbox](https://github.com/superradcompany/microsandbox) sur un Mac Apple Silicon, Linux (KVM) ou Windows arm64/x64 (Windows Hypervisor Platform / WHP) — **rien à installer** : codebox télécharge et vérifie l'exécutable `msb` automatiquement au premier démarrage ;
  - [Tart](https://github.com/openai/tart) (`brew install openai/tools/tart`) sur un Mac Apple Silicon pour les environnements de dev macOS (notamment Xcode / iOS) ;
  - [agent-vm](https://github.com/sylvinus/agent-vm) ([Lima](https://lima-vm.io) requis) sur macOS ou Linux : une VM Debian persistante par workspace, clonée depuis un template de base construit par codebox.

Le mode manuel (`MSB_PATH`) et les prérequis plateforme de Microsandbox sont détaillés dans [Runtime Microsandbox](runtime-microsandbox.md).

## Depuis les sources (contributeurs)

Prérequis : [Go](https://go.dev) >= 1.22 et une chaîne C native (Xcode Command Line Tools sur macOS, `gcc` sur Linux, MinGW-w64 x64 ou LLVM-MinGW sur Windows ; sous Windows arm64, la chaîne doit cibler `aarch64-w64-mingw32`).

```bash
go build -o codebox ./cmd/codebox
go test ./...
```
