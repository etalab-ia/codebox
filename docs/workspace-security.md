# Sécurité du workspace

**En mode Microsandbox, le checkout hôte n'est pas monté dans l'invité.** L'invité possède son propre espace de travail, un volume interne au sandbox : aucun fichier de l'hôte — `.env`, secret non commité, fichier ignoré — n'est lisible depuis l'agent. Le contenu ne traverse la frontière que par un **transfert filtré**, jamais par un montage.

## Transfert hôte → invité (filtre refus par défaut)

Chaque synchronisation — provisionnement initial et chaque rafraîchissement — résout un ensemble de fichiers concret, visible avant qu'un octet ne traverse. Sont **exclus par défaut** :

- les fichiers `.env` / `.env.*` (hors `.env.example` / `.env.sample`) ;
- les fichiers signalés par le détecteur de secrets que codebox fournit lui-même : [gitleaks](https://github.com/gitleaks/gitleaks) version 8.30.1, téléchargé à la première utilisation depuis la publication officielle. Deux empreintes SHA-256 figées dans le binaire sont vérifiées : celle de l'archive avant toute extraction, puis celle de l'exécutable après extraction et à chaque réutilisation ; le cache n'est jamais un lien symbolique et vit dans un répertoire d'état absolu. Rien n'est à installer sur le poste. Si le binaire ne peut pas être préparé (première utilisation hors ligne, par exemple), l'avertissement indique que la détection n'a pas tourné ; le filtre par nom, lui, s'applique toujours ;
- les fichiers ignorés par Git (c'est là que vivent les `.env` locaux et l'état dérivé) ;
- les symlinks (un lien peut résoudre hors de l'ensemble résolu) ;
- la métadonnée `.git` (le dépôt de l'invité est créé **dans** l'invité).

Le filtre est **structurellement inévitable** : le transfert écrit un payload unique construit depuis l'ensemble résolu, pas une copie d'arborescence. Un fichier refusé ne peut donc pas se retrouver dans l'invité.

```
codebox workspace status          # ce qui traverserait, ce qui est refusé et pourquoi
codebox workspace allow <chemin>  # réintégrer explicitement un fichier précis
codebox workspace deny <chemin>   # revenir au refus par défaut
codebox workspace sync [--force]  # rafraîchir l'espace invité
```

Il n'y a **pas** d'option « tout inclure » : une réintégration est une décision par fichier, enregistrée dans l'état hôte.

## Retour des changements

Les modifications faites dans l'invité ne reviennent jamais en écriture directe dans ton checkout :

```
codebox workspace export [--out <fichier>]
```

produit un patch (fichiers suivis et nouveaux) écrit dans l'état hôte, à relire puis appliquer avec `git apply`. Avec le grant GitHub activé, l'invité peut aussi livrer ses changements par branche/PR après revue ; aucun push n'est automatique.

**Non destructif :** `workspace sync` refuse de rafraîchir si l'invité contient du travail non commité ; il nomme les fichiers et n'écrase qu'avec `--force`.

## Instances héritées et scan d'hygiène

Une instance créée avant ce modèle porte un **montage lié** de l'hôte : son invité peut lire ton checkout. Elle n'est jamais convertie sur place — `codebox` refuse de la démarrer et indique la recréation explicite (`codebox clean --microsandbox` puis `start`).

Le scan de démarrage n'est plus une frontière de sécurité : il reste un **conseil d'hygiène** qui signale les fichiers ressemblant à des secrets dans le checkout. Sa détection est réutilisée à la frontière de transfert, où le refus est effectif.

**Tart et agent-vm montent toujours le workspace** : le modèle scellé ne s'y applique pas, et le scan y garde son rôle bloquant. Pour un projet qui doit exposer le checkout à l'invité, l'un de ces runtimes reste le choix explicite.

Les serveurs de dev lancés par l'agent sur les ports **3000-3010** sont accessibles depuis le navigateur de l'hôte : `http://localhost:3000`, etc. pour Microsandbox. Avec Tart, la VM macOS est une machine à part entière sur le réseau NAT : les previews et le TUI OpenCode utilisent l'adresse de la VM, par exemple `open "http://$(tart ip opencode-tahoe-base-latest):3000"`.
