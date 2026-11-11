# Guide de démarrage

Pour un premier lancement, exporte la clé Albert API dans ton terminal.

macOS / Linux :

```bash
export ALBERT_API_KEY="ta-clé"
codebox
```

Windows (PowerShell) :

```powershell
$env:ALBERT_API_KEY = "ta-clé"
codebox
```

`codebox` sans argument utilise **Microsandbox en isolation `full`** : l'agent, sa TUI et ses identifiants tournent dans la microVM, dont l'espace de travail est scellé (aucun fichier de l'hôte n'y est monté). `--tart` et `--agent-vm` restent disponibles explicitement, par flag ou via `RUNTIME`.

La commande prépare l'espace de travail de l'invité, démarre l'agent et attache la TUI OpenCode native.

Pour rendre la clé persistante et enregistrer le runtime, le workspace ou d'autres réglages, consulte la page [Configuration](config.md).

Le premier démarrage d'un sandbox Microsandbox installe ~384 Mio de paquets dans la microVM et peut dépasser largement une minute ; les démarrages suivants sont rapides.

## Prompts d'exemple

Une fois attaché, ces prompts exercent les dimensions clés de l'expérience :

1. **« Qu'y a-t-il dans ce workspace ? Décris la structure du projet. »** — inférence et outils fichiers de base.
2. **« Sur quel OS, noyau et architecture tournes-tu ? »** — confirme que l'agent s'exécute dans le sandbox Linux, pas directement sur ton Mac.
3. **« Code un petit jeu snake servi par un serveur Node sur le port 3000, puis lance-le. »** — écriture de fichiers + serveur de dev ; ouvre `http://localhost:3000` pour vérifier la preview.
4. **« Écris un script python qui affiche la suite de Fibonacci et exécute-le. »** — toolchain polyglotte dans le sandbox.
5. **« Modifie un fichier, puis annule ta modification. »** — outils d'édition et revue de diff.
6. **Détache-toi (Ctrl+C / quitte), conserve le runtime, puis relance `codebox --microsandbox` ou `codebox --tart`** — continuité de session et reconnexion.
7. **« Envoie les logs de ton serveur de dev dans /tmp/server.log et montre-moi les dernières lignes. »** — comportement du scratch space hors du répertoire projet.

## Aller plus loin

- [Utilisation](usage.md) : commandes, instances par projet, niveaux d'isolation.
- [Configuration](config.md) : assistant `setup`, variables d'environnement, skills, MCP.
- [Identifiants et secrets](credentials.md) : magasin d'identifiants et protection par runtime.
