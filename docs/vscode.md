# VS Code et ACP

`codebox acp` permet à un client ACP de piloter l'agent OpenCode qui tourne dans la microVM Microsandbox. Le poste fournit l'interface de l'éditeur ; l'agent, ses outils, ses identifiants et son espace de travail restent dans l'invité.

```text
VS Code → codebox acp → OpenCode ACP dans la microVM → /workspace
```

Cette interface est disponible uniquement avec **Microsandbox, l'isolation `full` et OpenCode**. Elle ne remplace ni le TUI ni le mode backend. Aucune installation d'OpenCode sur le poste n'est nécessaire.

## Prérequis

- Un projet configuré pour codebox. Si nécessaire, lancer `codebox init` dans un terminal avant d'ouvrir la session ACP.
- VS Code et l'extension [ACP Client](https://marketplace.visualstudio.com/items?itemName=formulahendry.acp-client), version 0.2.0 prise comme référence pour cette qualification.
- Node.js 18 ou plus récent, requis par l'extension.
- Le binaire `codebox` accessible depuis VS Code. Si l'extension ne le trouve pas, utiliser le chemin absolu affiché par `which codebox` dans la configuration ci-dessous.

La revue de référence de l'extension correspond à la révision `e7371659e3ac100db842b419b1361205a193032e`. Une autre version doit être revérifiée avant d'être présentée comme qualifiée.

## Configuration

Ouvrir les paramètres JSON de VS Code et ajouter ou fusionner cette configuration :

```json
{
  "acp.agents": {
    "Codebox": {
      "command": "codebox",
      "args": ["acp"],
      "env": {}
    }
  },
  "acp.autoApprovePermissions": "ask",
  "acp.defaultWorkingDirectory": "",
  "acp.logTraffic": false
}
```

- `autoApprovePermissions: "ask"` conserve une demande explicite pour les opérations qui utilisent les permissions ACP.
- `logTraffic: false` évite d'enregistrer les prompts, extraits de code et autres corps de messages dans le canal `ACP Traffic`.
- `defaultWorkingDirectory: ""` utilise le dossier ouvert dans VS Code. codebox remplace ensuite ce chemin par `/workspace` dans l'invité.
- `env` reste vide : ne pas mettre de clé API dans ce fichier.

L'extension journalise aussi des événements ordinaires dans le canal `ACP Client`, notamment certains chemins et titres d'outils. Ne pas utiliser de contenu sensible dans un parcours de démonstration.

## Démarrer une session

1. Ouvrir le dossier du projet dans VS Code.
2. Ouvrir le panneau `ACP Client` depuis la barre d'activité.
3. Ajouter ou sélectionner l'agent `Codebox`, puis démarrer une conversation.
4. Patienter pendant la préparation de la microVM. Le premier démarrage peut dépasser une minute ; les messages de préparation restent dans les diagnostics, pas dans le flux ACP.
5. Commencer par une demande en lecture seule, par exemple : « Décris la structure de ce projet sans modifier de fichier. »

La commande `codebox acp` doit être lancée par l'extension, pas dans un terminal interactif : elle attend des messages JSON-RPC sur son entrée standard.

## Modifications et retour vers le projet

L'agent modifie `/workspace` dans l'invité. Il ne modifie pas directement le checkout affiché par VS Code.

Pour récupérer les changements, utiliser dans un terminal :

```sh
codebox workspace export
```

Relire le patch produit, puis l'appliquer explicitement avec `git apply`, ou utiliser le flux branche/PR si le projet l'autorise. Aucune synchronisation automatique du checkout n'a lieu.

## Frontière de sécurité

- Le client ne peut pas activer les lectures ou écritures de fichiers du poste via ACP.
- Le client ne peut pas demander un terminal sur le poste ni utiliser l'authentification par terminal.
- Les racines supplémentaires et les serveurs MCP envoyés par le client sont retirés des requêtes.
- Les permissions `ask` ne remplacent pas la microVM : elles ne constituent pas une frontière contre une commande shell ou un script arbitraire.
- L'éditeur et l'extension restent capables de lire le checkout du poste. Installer uniquement des extensions approuvées.

## Dépannage

- **`codebox` est introuvable :** utiliser son chemin absolu dans `acp.agents.Codebox.command`, puis redémarrer l'agent depuis le panneau ACP.
- **Le projet n'est pas configuré :** quitter la session ACP, lancer `codebox init` dans un terminal, puis rouvrir le dossier.
- **La réponse JSON-RPC est invalide :** vérifier qu'aucun wrapper ne remplace `codebox acp` en écrivant des messages sur stdout. La préparation normale écrit uniquement sur stderr.
- **L'agent ne démarre pas :** consulter le canal `ACP Client` et lancer `codebox logs` dans un terminal. Ne pas réactiver `logTraffic` sur un projet contenant des informations sensibles.

## Qualification native VS Code

Effectuer ce parcours sur une machine Microsandbox provisionnée, avec un projet jetable et une clé de test révocable. Ne mettre aucune clé dans les captures ou le rapport.

1. Noter la date, l'OS, l'architecture, la révision codebox et la version de l'extension. Lancer `codebox version`.
2. Appliquer la configuration ci-dessus avec `logTraffic: false` et `autoApprovePermissions: "ask"`.
3. Ouvrir le projet jetable, sélectionner `Codebox` et créer une session. Vérifier qu'elle démarre sans installation d'OpenCode sur le poste.
4. Envoyer « Décris la structure de ce projet sans modifier de fichier ». Vérifier que la réponse arrive en flux et que les outils travaillent dans `/workspace`.
5. Lancer une tâche plus longue puis utiliser `ACP: Cancel Current Turn`. Vérifier que la tâche s'arrête et que l'interface redevient disponible.
6. Fermer puis rouvrir le panneau ou l'agent. Vérifier que la session précédente peut être reprise sans créer une seconde microVM pour le même projet.
7. Demander la création de `ACP_CANARY.md` avec un contenu non secret. Vérifier que ce fichier n'apparaît pas dans le checkout du poste.
8. Lancer `codebox workspace export`, relire le patch et confirmer qu'il contient le canari. Avant toute application explicite, vérifier que `git status` sur le poste ne montre aucune modification de l'agent.
9. Arrêter et supprimer la microVM jetable, puis supprimer les répertoires de qualification. Conserver uniquement un rapport redacté : versions, résultats de chaque étape, anomalies et preuves de nettoyage.

Le protocole ACP sans interface graphique est qualifié séparément par les tests Go et le handshake natif `initialize` + `session/new`. Une réussite de ces tests ne remplace pas le parcours VS Code ci-dessus.
