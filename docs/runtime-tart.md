# Runtime Tart

Le bootstrap installe **OpenCode v2.2.5** via `@opencode/cli` ou l'installateur v2 épinglé. Une ancienne installation est mise à jour en place avant de lancer le serveur ou le TUI, sans recréer la VM. Le répertoire privé `~/.opencode/bin` est prioritaire sur le binaire v1 éventuellement fourni par l'image. En mode full, le TUI utilise `--standalone` pour ne pas laisser un service autonome après sa fermeture.

## Réseau Tart et VPN

`TART_MTU=1280` est la valeur par défaut pour le réseau invité Tart. Elle limite les blocages TLS observés avec certains VPN sur le Mac hôte, sans garantir la compatibilité avec tous les VPN. Le bootstrap applique cette valeur à l'interface de route IPv4 par défaut avant toute installation de logiciel ; seul le réseau de la VM est modifié. Une MTU réduite peut légèrement diminuer les performances réseau.

La variable `TART_MTU` accepte un entier de **1280 à 1500**, ou **`auto`** pour ne pas modifier la MTU. `auto` ne restaure pas une valeur précédemment appliquée ; utiliser `1500` pour revenir à la valeur habituelle. L'application nécessite `sudo` sans mot de passe dans l'invité (disponible dans l'image de base utilisée). Les erreurs sont consignées dans `just-code logs --tart`.

Après modification, exécuter **`just-code stop` puis `just-code --tart`** pour réappliquer le réglage tout en conservant les logiciels installés. Un backend déjà sain n'est pas reconfiguré par `just-code start`. `just-code restart --tart` est également non destructif ; `just-code recreate --tart` reconstruit la VM.

Sur macOS, autoriser également le terminal utilisé dans **Réglages Système > Confidentialité et sécurité > Réseau local**. Cette permission couvre l'accès à l'adresse privée de la VM, même si elle tourne sur le même Mac. Sans elle, le TUI peut rester en attente ou signaler une erreur trompeuse d'URL/port.

## Images Tart et nommage des VM

L'image par défaut est **macOS Tahoe** (`:latest`) :

```dotenv
TART_IMAGE=ghcr.io/cirruslabs/macos-tahoe-base:latest
```

Le nom de la VM dérive du nom de fichier de la référence image (`:` et `@sha256` remplacés par `-`, préfixe `opencode-`) :

- **Tahoe** (`:latest`) : `opencode-tahoe-base-latest`
- **Sonoma** (`:latest`) : `opencode-sonoma-base-latest`

Deux images partageant le même nom de fichier (par exemple issues de registres différents) produiraient le même nom de VM. Les images Cirrus Labs de Tahoe et Sonoma ont des noms de fichier distincts et constituent le cas pris en charge.

Le préfixe `opencode-` identifie les VM gérées par just-code : `just-code stop` arrête toutes les VM Tart locales portant ce préfixe. Ne pas le réutiliser pour des VM créées en dehors de just-code.

Pour basculer vers Sonoma :

```dotenv
TART_IMAGE=ghcr.io/cirruslabs/macos-sonoma-base:latest
```

Les VM Tahoe et Sonoma coexistent ; changer `TART_IMAGE` cible l'autre VM sans supprimer la précédente. Utiliser `tart list` pour voir les VM disponibles et `tart delete <nom>` pour libérer l'espace.

**Note** : Tahoe est nécessaire pour Xcode 26.3+. Sonoma ne supporte que Xcode 16.2 et versions antérieures.
