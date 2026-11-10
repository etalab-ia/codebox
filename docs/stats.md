# Statistiques d'usage (avec consentement)

just-code peut partager avec l'équipe des informations **non nominatives** sur
les choix faits dans l'assistant, pour orienter le produit. Le partage est
**désactivé par défaut** : rien n'est construit, mis en file ni envoyé sans un
« o » explicite.

## Consentement

La question est posée une seule fois, à la fin de `just-code setup`, juste
avant le récapitulatif. Elle vaut aussi pour les `init` suivants. Le
récapitulatif indique `Statistiques d'usage : partagées` ou `non partagées`.

En mode non interactif (entrée piped), la question n'est pas posée — la
prochaine ligne appartient à un autre écran — et le partage reste désactivé.
Utiliser `just-code stats on` pour l'activer explicitement.

## Commandes

```bash
just-code stats on      # accepte le partage
just-code stats off     # arrête tout envoi et vide la file locale
just-code stats show    # affiche exactement ce qui est (ou serait) envoyé
```

`stats show` affiche l'état du consentement, la configuration de l'endpoint,
les identifiants locaux et le contenu de la file d'envoi, événement par
événement, en JSON. C'est la représentation exacte de l'envoi.

## Données envoyées

Chaque événement porte un identifiant aléatoire d'événement, la version de
just-code, le système et l'architecture, un identifiant aléatoire de poste et,
pour un `init`, un identifiant aléatoire de projet. Ces identifiants sont
générés localement ; ils ne sont dérivés ni d'un nom, ni d'un chemin, ni d'une
identité.

Champs collectés :

- `setup` : plateformes connectées (type uniquement), modèles cochés, modèle
  par défaut, présence d'un jeton GitHub (0/1), présence d'une identité git
  (0/1) ;
- `init` : chaque skill (0/1), chaque MCP (0/1), cas GitHub 1 à 4, réglages
  avancés ouverts (0/1), harnais, runtime, isolation, stockage des skills ;
- abandon : dernière étape atteinte quand l'assistant est quitté.

Un abandon n'est enregistré que si la question de consentement a déjà reçu une
réponse (y compris un refus) : quitter un assistant avant d'atteindre cet écran
ne produit rien, même si le partage a été activé plus tard avec `stats on`.

Ne sont **jamais** collectés : code, prompts, réponses de l'agent, noms de
projet ou de dépôt, chemins, clés, jetons, identité git, adresse IP.
Le nom saisi pour une plateforme compatible OpenAI n'est pas envoyé : seul son
type l'est.

## Stockage et envoi

Les événements sont mis en file dans l'état local du poste
(`stats/queue.json`), sous permissions `0600`, avec une limite de 32
événements. L'envoi se fait dans un processus détaché, avec un délai court :
un endpoint injoignable ne ralentit ni ne bloque l'assistant, et un échec
laisse la file intacte pour une tentative ultérieure.

L'endpoint est géré par l'équipe just-code. Cette version n'en configure aucun
par défaut : sans endpoint, les événements restent locaux. Une construction de
release peut le fixer avec
`-ldflags "-X github.com/etalab-ia/just-code/internal/justcode.StatsEndpointDefault=<url>"`,
et `JUST_CODE_STATS_URL` le remplace pour les tests et le développement.

La valeur de release est lue dans la variable de dépôt GitHub
`JUST_CODE_STATS_URL` (Settings → Secrets and variables → Actions → Variables).
La modifier change la destination des prochaines releases sans toucher au code ;
variable absente (fork, dépôt tiers), aucune URL n'est intégrée et rien n'est
envoyé.

Valeur utilisée aujourd'hui :

```
https://grist.numerique.gouv.fr/o/albert/api/docs/dUBCQK1eFcUHrdGAcViVyZ/tables/Events/records
```

C'est l'identifiant **stable** du document, et non l'identifiant d'URL
`dUBCQK1eFcUH` : ce dernier change si le document est renommé ou redéplacé,
alors que le premier est invariant. Les deux fonctionnent aujourd'hui, mais
seul le second survit à un renommage.

Deux formats d'envoi existent, et c'est l'URL de l'endpoint qui décide lequel
s'applique — les deux valeurs ne peuvent donc pas se contredire :

- une collection `records` d'une table Grist
  (`https://grist.numerique.gouv.fr/o/<org>/api/docs/<docId>/tables/<tableId>/records`)
  reçoit un enregistrement par événement et **aucun justificatif** ;
- toute autre URL reçoit l'enveloppe JSON groupée `{"schemaVersion", "events"}`
  décrite plus haut, qui est le contrat d'un relais géré par l'équipe.

Les deux exigent TLS (sauf boucle locale, pour les tests).

## Recevoir les statistiques dans Grist

Le client n'a besoin d'aucune clé : le document Grist est public, et ses règles
d'accès n'autorisent qu'une seule chose, la création d'enregistrements dans la
table de réception. C'est le mécanisme publié par `gristlabs/grist-form-submit`,
dont notre client suit le format.

1. Créer un document dans l'organisation de l'équipe, avec une table `Events`.
2. Ajouter les colonnes, **en identifiant les colonnes par leur identifiant
   technique** (pas leur libellé) : `eventId`, `at`, `kind`, `machineId`,
   `projectId`, `schemaVersion`, `version`, `os`, `arch`, `payload`.
   `payload` est du texte (le JSON brut de l'événement).
3. Ouvrir les règles d'accès, puis :
   - décocher « Allow editors to edit structure » ;
   - dans les règles par défaut, ajouter la condition `user.Access != OWNER`
     avec toutes les permissions refusées (`R U C D`) ;
   - ajouter des règles de table pour `Events`, condition vide (« Everyone »),
     et n'accorder que `C` (création) ;
   - ajouter le garde-fou anti-doublon décrit plus bas : c'est une seconde
     règle, placée **au-dessus** de celle qui autorise `C`.
4. Partager le document en accès public avec le rôle `Editor`. Les règles
   ci-dessus annulent tout ce que ce rôle permettrait d'autre.
5. Vérifier depuis une fenêtre de navigation privée : le document s'ouvre mais
   paraît vide ; l'envoi d'un enregistrement réussit ; la lecture, la
   modification et la suppression échouent.

Le point 5 est la vérification qui compte. `stats show` affiche l'endpoint
configuré, ce qui rend l'envoi observable, et `just-code stats off` l'arrête.

### Ce que ce montage implique

- **Les données sont illisibles sans authentification.** Un visiteur anonyme
  n'a aucune permission de lecture sur `Events` : il peut ajouter un
  enregistrement, pas en lire un seul. Les statistiques ne sont visibles que des
  membres du document.
- **Rien ne garantit qu'une écriture vienne de just-code.** N'importe qui peut
  ajouter un enregistrement à la table, y compris des valeurs inventées. Les
  événements doivent donc être traités comme des données non fiables à
  l'analyse, et l'absence de limitation de débit sur un service public est
  assumée à ce stade.
- **Les journaux du service Grist ne sont pas sous notre contrôle.** L'exigence
  de non-conservation des adresses IP (#181) n'est donc pas satisfaite par ce
  montage. Il est retenu comme étape transitoire : grist.numerique.gouv.fr est
  un service interministériel hébergé en SecNumCloud en France, et non un
  sous-traitant tiers, mais cela ne remplace pas la vérification prévue par le
  relais.
- **Les doublons après réessai.** Réessayer un événement déjà écrit se heurte au
  refus de la règle d'accès, et Grist répond alors `403` avec un message
  `Blocked by … access rules`. Le client traite ce refus précis comme « déjà
  enregistré » et retire l'événement de la file, pour qu'un réessai ne bloque
  pas les suivants jusqu'à la limite de 32. Un `403` qui ne vient pas des règles
  d'accès reste, lui, réessayé.

  Pour compter sans doublon, ajouter le garde-fou publié par Grist : une colonne
  de formule `Count` valant `len(Events.lookupRecords(eventId=$eventId))`, et
  une règle d'accès qui refuse `C` (et `U`) quand `newRec.Count > 1` — donc sur
  la ligne proposée, pas sur celle qui existe déjà. **Sans cette règle, un
  réessai crée une ligne en double** : le client ne peut pas dédupliquer seul,
  faute de droit de lecture.

  Aucune configuration supplémentaire n'est nécessaire : Grist nomme le type de
  règle qui refuse l'action, et c'est ce qui distingue les deux cas.

  ```
  Blocked by row create access rules      <- le garde-fou (condition sur newRec)
  Blocked by table create access rules    <- une règle de table ou par défaut
  ```

  Le garde-fou portant sur `newRec`, il est forcément **au niveau ligne**,
  alors qu'une permission mal configurée ou temporairement retirée refuse au
  niveau **table**. Le client retire donc l'événement sur le premier message et
  le conserve sur le second. C'est vérifié sur l'instance réelle.

Le consentement est relu avant **chaque** écriture, pas une fois pour le lot :
un `just-code stats off` lancé pendant un envoi arrête les suivants.

### Ce que le client fait des réponses en erreur

- **`2xx`** : l'événement est retiré de la file.
- **`403` de niveau ligne** (`Blocked by row … access rules`) : le garde-fou
  anti-doublon a refusé, l'événement est déjà enregistré et retiré de la file.
- **Tout le reste** (dont `400`, `403` de niveau table, `5xx`, réseau) : la file
  est conservée et l'erreur est remontée, avec le message de Grist lorsqu'il y
  en a un.

Le `400` est un cas de configuration, pas un rejet d'événement : Grist le renvoie
quand la requête ne correspond pas à la table (`Invalid column "…"`, `Can't save
value to formula column …`), ce qui touche tous les événements de la même
façon. Les retirer un par un détruirait l'ensemble des données sans rien
signaler ; conservés, ils repartent dès que la table est corrigée.

`eventId` doit donc rester une **colonne de données** (texte) : c'est le jeton
généré par le client, celui qui permet de reconnaître un renvoi. Une colonne de
formule `UUID()` est calculée par Grist, ne peut pas recevoir cette valeur (le
client reçoit alors un `400`), et surtout change à chaque ligne — elle rend donc
toute déduplication impossible. Grist fournit déjà un identifiant unique par
ligne sans qu'on ait à le créer.
