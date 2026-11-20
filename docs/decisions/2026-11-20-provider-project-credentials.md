# 2026-11-20 : provider et références d'identifiants par projet

## Contexte

#445 fait du provider un choix de projet, avec des clés réutilisables depuis le
poste. Avant #460, la liste des providers vivait dans les réglages utilisateur et
`credentialRef` ne s'appliquait qu'à Albert. Une référence arbitraire ne portait
pas de destination : l'utiliser pour Mistral ou un provider compatible OpenAI
aurait pu associer une clé au mauvais hôte.

## Décision

- Chaque entrée `providers` peut porter `credentialRef`, toujours sans valeur.
  Les réglages utilisateur passent en schéma 2. Le manifeste projet enregistre
  les providers choisis et passe en schéma 8 ; les binaires qui ignorent ce champ
  doivent refuser le fichier plutôt que choisir silencieusement un autre provider.
- Albert conserve ses références personnalisées historiques : son hôte est fixe et
  connu du code. Pour les autres providers, la référence nouvellement enregistrée
  est dérivée du provider et de l'hôte canonique : `<kind>:<host>`. Le `:` évite
  toute collision avec le format persistant `entrée@magasin#association` utilisé
  pour la rotation et la révocation. Une référence
  qui nomme un autre provider ou hôte est refusée avant toute lecture du magasin.
- Une référence explicite est prioritaire sur la variable d'environnement et
  l'entrée fixe legacy. Une référence absente du magasin est une erreur, pas une
  autorisation de retomber sur une autre valeur. `CODEBOX_CREDENTIAL_REF` garde
  sa priorité Albert existante dans la chaîne de résolution.
- Sans référence explicite, la chaîne historique reste inchangée : variable
  d'environnement puis entrée fixe. Aucune copie automatique de clé n'est faite
  lors de la migration. Les anciens projets et les anciennes installations
  continuent donc à résoudre leurs identifiants actuels.
- La récupération d'une microVM conserve ses associations persistées. Si le
  provider persisté n'est plus configuré, la récupération utilise l'entrée fixe
  legacy de ce provider ; elle n'exige pas un provider sans rapport avec la VM.
- Le parcours combiné de #461 consomme ce contrat. Le changement ne crée ni un
  second magasin ni un registre de destinations séparé de la configuration du
  provider.

## Conséquences et vérification

- Les fichiers nouvellement écrits exigent une version de codebox qui comprend
  les références par provider ; c'est une rupture volontaire et explicite.
- Les tests couvrent la propagation dans le manifeste, la réutilisation à hôte
  identique, le refus inter-provider ou inter-hôte avant lecture du magasin, la
  priorité des références explicites, les fallbacks legacy et la récupération
  avec un provider différent.
- Les tests de setup vérifient qu'une correction d'endpoint recompute la
  référence pour le nouvel hôte et que la clé n'est jamais écrite sous l'ancien.
- La qualification native vérifie ensuite le démarrage réel avec au moins deux
  projets qui réutilisent la même clé à hôte identique et avec une référence
  volontairement incorrecte qui doit être refusée.
