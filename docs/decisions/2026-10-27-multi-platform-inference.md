# D-003 : plateformes d'inférence multiples (Albert API, Mistral AI, compatible OpenAI)

Date : 27 octobre 2026
Statut : accepté
Suivi : #180 (épic #160)

## Contexte

Jusqu'ici, codebox ne connaissait qu'Albert API : une clé, un catalogue
curated, un provider OpenCode `albert`, un seul hôte autorisé dans le proxy de
secrets. L'épic #160 demande de connecter au moins une plateforme parmi Albert
API, Mistral AI et une plateforme compatible OpenAI, avec un écran de clé par
plateforme et une sélection de modèles groupée.

Le point sensible est le transport des identifiants : le proxy Microsandbox ne
substitue une valeur que vers les hôtes explicitement autorisés pour cette
liaison. Une URL de plateforme libre devient donc une décision de sécurité, pas
un simple champ de configuration.

## Décision

1. **Un modèle de réglages, pas de secret.** `settings.json` gagne une liste
   `providers` : `{kind, name, baseURL}`. Les valeurs de clé restent dans le
   magasin d'identifiants, sous les entrées `albert`, `mistral` et
   `openai-compatible`.
2. **Au moins une plateforme, Albert par défaut.** Une liste vide signifie
   Albert API seule, ce qui conserve le comportement des réglages existants.
   Albert API est proposée en premier et pré-cochée, mais peut être décochée.
3. **URL fixe pour Albert et Mistral.** Une URL personnalisée est refusée pour
   ces deux plateformes : une faute de frappe ne doit pas pouvoir envoyer une
   clé souveraine à un autre hôte.
4. **Règle pour un hôte libre (compatible OpenAI).** L'URL de base doit être en
   `https`, sans identifiants, requête, fragment ni port non standard ; son
   hôte doit être un nom DNS. Cet hôte devient la seule destination autorisée
   pour la clé de cette plateforme. Les hôtes des plateformes configurées
   doivent être disjoints, comme pour les liaisons existantes.
5. **Une clé par plateforme, une variable invitée par plateforme.**
   `ALBERT_API_KEY`, `MISTRAL_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`. Sur
   Microsandbox, chaque variable est liée à son seul hôte par le proxy. Sur
   Tart et agent-vm, les clés sont déposées dans l'invité en clair, sous
   l'acquittement existant.
6. **Références de modèles.** Une référence est `plateforme/modèle` pour
   Mistral et compatible OpenAI. Une valeur sans préfixe géré reste un
   identifiant Albert, y compris quand il contient un `/`
   (`openai/gpt-oss-120b`). Le provider OpenCode reprend le préfixe géré :
   `albert`, `mistral`, `openai-compatible`.
7. **Le bloc Albert embarqué n'est pas toujours exposé.** L'asset OpenCode
   contient un provider `albert`. Quand Albert n'est pas connectée mais qu'une
   autre plateforme l'est, ce bloc est retiré de la configuration composée :
   afficher des modèles dont la clé n'est pas liée produirait des choix cassés.
8. **Import.** `codebox import albert-code` reprend une configuration
   OpenRouter existante comme plateforme compatible OpenAI (URL et modèles),
   et rappelle que la clé se stocke avec `codebox auth add
   openai-compatible`. Aucune valeur n'est lue dans le fichier importé.

## Conséquences

- Le wizard interactif demande les plateformes, puis une clé et une
  vérification par plateforme cochée. Chaque écran non-Albert affiche
  l'avertissement hors infrastructure de l'État.
- Les erreurs de vérification sont distinctes : clé refusée, endpoint
  injoignable, réponse qui n'est pas un catalogue de modèles.
- Le catalogue Albert reste curated et mis en cache ; les autres plateformes
  sont lues à l'exécution, sans liste figée.
- `codebox models` liste les modèles de chaque plateforme connectée et
  marque ceux qui sont cochés.
- Un hôte libre reste une décision explicite de l'utilisateur : codebox
  n'ouvre que cet hôte, jamais un domaine générique.
