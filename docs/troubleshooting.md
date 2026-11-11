# Dépannage

## Le backend ne devient jamais healthy

Une VM Microsandbox survit à un redémarrage, mais son entrée de conteneur (`/.msb/scripts/start`, qui lance `opencode serve`) ne s'exécute qu'à la **création**. Une VM qui revient au démarrage est donc `running` sans aucun processus OpenCode : « VM démarrée » n'est pas « backend prêt ». C'est la cause la plus fréquente d'un backend qui ne devient jamais healthy.

`codebox` en tient compte et se répare : si le sandbox tourne mais que le backend ne répond pas, l'entrée est relancée dans la microVM ; s'il est arrêté, il est démarré puis l'entrée est relancée (un simple redémarrage de la VM ne rejoue pas l'entrée). Le même correctif s'applique à Tart, où le processus OpenCode obsolète est tué avant relance.

Si malgré cela le backend ne répond pas, l'attachement (`codebox` sans commande) attend en affichant l'avancement (300 s par défaut, réglable via `CODEBOX_START_TIMEOUT` en secondes). À l'expiration, l'erreur précise le dernier résultat observé, ce qui distingue les causes :

- `HTTP 401: unauthorized` — le mot de passe attendu par le backend diffère de `OPENCODE_SERVER_PASSWORD`. Un sandbox créé lors d'un run précédent conserve l'ancien mot de passe. Utiliser `codebox recreate --<runtime>` (destructif) ou `codebox stop` puis `codebox --<runtime>`.
- `connection refused` — le backend n'écoute pas ; `codebox logs --<runtime>` montre la sortie du bootstrap invité.
- `HTTP 200: ...` sans `healthy` — l'application démarre encore ; attendre quelques secondes.

Le premier démarrage d'un sandbox Microsandbox installe ~384 Mio de paquets dans la microVM et peut dépasser largement une minute ; les démarrages suivants sont rapides (l'installation est marquée dans `/var/lib/codebox/toolchain-ready`).

Le réflexe le plus simple reste `codebox stop` suivi d'un relancement de `codebox --<runtime>`.

## Conteneur Docker d'une version précédente

Les versions antérieures à la suppression du runtime Docker laissaient un conteneur `albert-opencode-sandbox` (`restart: unless-stopped`) qui publiait les ports 4096 et 3000-3010. Au premier `start` / `attach`, et sur `codebox stop`, codebox détecte ce conteneur et le supprime automatiquement en l'annonçant ; s'il ne peut pas le supprimer, il s'arrête en le signalant plutôt que de laisser un conflit de ports inexpliqué. Aucun `docker` installé n'est nécessaire : l'absence de Docker est ignorée.

