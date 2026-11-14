# Identifiants et secrets

## Protection des identifiants par runtime

Le niveau d'isolation ne détermine pas à lui seul ce qu'un agent peut lire : c'est le runtime qui décide si la clé Albert est substituée à la frontière réseau ou déposée en clair dans l'invité.


| Runtime | Injection protégée | Portée |
|---|---|---|
| Microsandbox | Oui, en `backend` **et** en `full` | La clé n'existe que côté hôte ; l'invité reçoit le placeholder `$MSB_ALBERT_API_KEY`, remplacé par le proxy réseau uniquement vers `albert.api.etalab.gouv.fr` |
| Tart | Non | La clé est lisible dans l'invité, en `backend` comme en `full` |
| agent-vm | Non | Idem |

Les clés Mistral AI et de la plateforme compatible OpenAI suivent la même règle
par runtime : substituées par le proxy vers leur seul hôte sur Microsandbox,
lisibles dans l'invité sur Tart et agent-vm (l'acquittement
`--acknowledge-guest-credentials` couvre l'ensemble des clés d'inférence).

Sur Microsandbox, le secret est déclaré via l'API de secrets du runtime et la substitution réseau se fait à la frontière : passer en `full` place le TUI dans la microVM sans exposer la clé pour autant. Un sandbox créé par une version antérieure, qui persistait la clé en clair dans l'environnement invité, est refusé au démarrage avec la commande de recréation à lancer ; l'ancienne valeur ne peut pas être remplacée sur place. Si l'environnement invité ne peut pas être relu, le démarrage est refusé de la même façon : sans cette lecture, rien ne distingue un sandbox sain d'un sandbox qui expose encore la clé.

Sur Tart et agent-vm, les secrets ne passent jamais par la ligne de commande : en mode `full` ils sont transmis sur l'entrée standard (Tart) ou par un fichier `0600` copié dans l'invité (agent-vm), et le TUI les lit depuis ce fichier. Le fichier porte aussi la configuration du provider Albert, sans quoi le TUI n'aurait pas de modèle à utiliser. Ces deux runtimes n'ont pas de proxy audité : la clé y est en clair dans l'invité, quel que soit le niveau d'isolation. Le démarrage l'exige donc explicitement : sans `--acknowledge-guest-credentials`, `start` est refusé ; avec, l'avertissement reste affiché à chaque démarrage. Réservez-les aux travaux qui n'ont pas besoin de la clé, ou traitez l'invité comme portant un identifiant vivant.


## Stockage global des identifiants (`auth`)

`codebox auth` stocke les identifiants globaux dans le magasin natif de l'OS — Keychain macOS, Gestionnaire d'identifiants Windows, Secret Service Linux — jamais dans un fichier projet ni un profil shell. Les commandes :

```bash
codebox auth add [albert|mistral|openai-compatible|github|context7]   # saisie masquée interactive (ou --stdin pour un pipe)
codebox auth status                         # état du magasin, jamais les valeurs
codebox auth remove [albert|mistral|openai-compatible|github|context7] # révocation puis suppression
codebox auth use-fallback [albert|mistral|openai-compatible|github|context7] # enregistre qu'une valeur déjà stockée vit dans le fichier
```

Le secret ne passe jamais par la ligne de commande (argv) : la saisie interactive est masquée, `--stdin` lit une ligne sur l'entrée standard. Sur les hôtes sans magasin natif (Linux headless sans Secret Service), le repli `--fallback` écrit un fichier JSON `0600` dans le répertoire de configuration — **jamais créé implicitement** : sans consentement explicite (`auth add --fallback`), toute écriture échoue.

L'ordre de résolution de la clé au démarrage est détaillé plus bas. Les identifiants restent référencés par nom (`credentialRef`) dans la configuration gérée ; la valeur ne figure jamais dans `settings.json`, `project.json` ni les exports.

Un magasin natif indisponible ou verrouillé est une erreur explicite au démarrage, jamais un repli silencieux vers le fichier : ce fichier peut détenir un identifiant périmé ou différent précisément quand l'attendu ne peut pas être vérifié. Seul un « introuvable » (aucune entrée) poursuit vers le repli consentit.

Pour que ce contrat ne rende pas inatteignable une valeur que vous avez délibérément placée dans le fichier, chaque écriture **enregistre** le magasin qui l'a reçue (état hôte, `credential-stores.json`, sans aucune valeur). Au démarrage, un identifiant enregistré comme stocké dans le fichier est lu **directement dans le fichier**, sans sonde du magasin natif : sur un hôte sans session D-Bus, un `secret-tool` incapable de démarrer ne bloque donc plus la lecture d'une valeur que vous avez stockée avec `--fallback`. Un identifiant enregistré comme natif ne change rien, puisque le magasin natif est déjà en tête de l'ordre ci-dessous ; et sans enregistrement, l'ordre documenté s'applique tel quel, erreur explicite comprise. `auth use-fallback` enregistre ce choix pour une valeur déjà présente dans le fichier, sans redemander le secret.

La rotation est détectée sans jamais toucher la valeur : `auth add` incrémente un compteur par identifiant (état hôte, non secret), et la réconciliation compare ce compteur — remplacer une clé sur une instance saine planifie un rafraîchissement au lieu du no-op.


## Résolution de la clé Albert

Au démarrage d'un runtime, la clé Albert est résolue dans l'ordre :

1. `credentialRef` : `CODEBOX_CREDENTIAL_REF` > `credentialRef` du
   manifeste projet > celui des réglages utilisateur. Une référence qui ne
   nomme rien dans le magasin est une erreur explicite, jamais un repli
   silencieux.
2. La variable d'environnement `ALBERT_API_KEY`.
3. L'identifiant `albert` du magasin.

Sur Microsandbox, la valeur n'est jamais persistée : la liaison est une
référence à une variable d'environnement hôte re-résolue à chaque démarrage
(voir [le document de décision dédié](decisions/2026-09-24-microsandbox-credential-transport.md)).
Les liaisons optionnelles (`github`, `context7`) exigent une
approbation par projet (`codebox bindings approve`), enregistrée dans
l'état local de l'hôte — jamais dans le dépôt.


## Liaisons d'identifiants (`bindings`)

Sur Microsandbox, les identifiants sont injectés par le proxy de secrets : la valeur brute n'est jamais persistée (ni dans la base du runtime, ni dans l'environnement invité) — la liaison est une référence à une variable d'environnement hôte, re-résolue à chaque application et à chaque démarrage. Chaque plateforme connectée a une liaison obligatoire, limitée à son propre hôte : `ALBERT_API_KEY` vers `albert.api.etalab.gouv.fr`, `MISTRAL_API_KEY` vers `api.mistral.ai`, `OPENAI_COMPATIBLE_API_KEY` vers l'hôte de l'URL de base saisie. Ces hôtes sont disjoints : une clé ne peut jamais être substituée vers la destination d'une autre plateforme. `github` et `context7` restent **optionnelles et approuvées projet par projet** :

```bash
codebox bindings list              # liaisons connues, approbations, état du magasin
codebox bindings approve github    # autorise la liaison github pour ce projet
codebox bindings revoke github     # retire l'approbation et révoque l'instance du projet
```

L'approbation est un enregistrement local à l'hôte (`~/.local/state/codebox/instances/<instance>/bindings.json`) : elle n'est jamais versionnée et ne voyage pas avec un clone. Les hôtes autorisés sont disjoints entre liaisons, donc une liaison ne peut pas être substituée vers la destination d'une autre.

## GitHub dans l'invité

Le workflow est optionnel et limité à Microsandbox. Stocke d'abord le jeton,
puis active le workflow pendant l'initialisation du projet :

```bash
codebox auth add github
codebox init --github
```

L'approbation reste locale à l'hôte ; elle n'apparaît ni dans le manifeste ni
dans le dépôt. Pour un projet déjà initialisé, utilise
`codebox bindings approve github` après avoir stocké le jeton. Sans
approbation locale, `gh` n'est pas installé par codebox. Pour désactiver un
accès déjà approuvé : `codebox bindings revoke github`.

L'étape GitHub de `init` vérifie sur son propre écran et affiche un écran
distinct par situation :

- **dépôt et jeton avec les bons droits** : le dépôt détecté et le compte du
  jeton sont affichés, puis la question d'autorisation, qui répond **Oui** par
  défaut. Les droits annoncés sont réellement vérifiés : la lecture du dépôt,
  le rôle d'écriture, et la permission Pull requests dont dépend la pull
  request en brouillon promise à l'écran. Le rôle d'écriture ne suffit pas à
  lui seul : un jeton peut pousser sur le dépôt sans pouvoir ouvrir de pull
  request ;
- **projet non relié à GitHub** : l'écran explique que les modifications se
  récupèrent avec `codebox workspace apply`, et propose de continuer ou de
  quitter pour relier le projet ;
- **dépôt détecté mais aucun jeton enregistré** : l'écran propose d'ajouter un
  jeton maintenant (même écran qu'au `setup`) ou de le faire plus tard ;
- **vérification impossible** : réseau injoignable ou quota GitHub atteint.
  L'écran le dit et propose `Revérifier`, plutôt que de conclure à tort à un
  problème de droits ;
- **jeton présent mais droits insuffisants** : l'écran distingue quatre causes
  — le jeton n'a pas accès au dépôt, il lui manque Contents et Pull requests en
  écriture, l'organisation n'a pas encore approuvé le jeton, ou GitHub ne
  l'accepte plus — puis propose `Revérifier` ou `Continuer sans GitHub`.

`Revérifier` relance la vérification sans quitter l'assistant : une correction
faite sur GitHub est prise en compte immédiatement, et aucune réponse déjà
donnée n'est perdue. `--github` applique la même vérification avant d'écrire
quoi que ce soit : sans `origin`, sans jeton, ou avec des droits insuffisants,
la commande échoue en nommant la cause.

Au premier lancement approuvé, codebox installe GitHub CLI 2.100.0 dans la
microVM uniquement, vérifie son empreinte, puis teste l'authentification,
l'accès au dépôt et le rôle d'écriture du compte. Le checkout hôte n'est jamais monté :
la branche invitée est basée sur la branche par défaut du dépôt. **La préparation
refuse tout fichier nouveau ou modifié de la copie filtrée par rapport à cette
branche distante.** Elle liste les chemins concernés et ne crée ni commit
automatique ni branche publiable. Copier un fichier dans la machine isolée
n'autorise pas sa publication sur GitHub, même si le filtre de secrets l'accepte.
Cela vaut aussi pour `.codebox/project.json`, `.codebox/lock.json` et
`AGENTS.md` créés par `init`, et pour le contenu de commits locaux non poussés.

Relis les fichiers sur ton poste et décide séparément de leur publication.
La préparation GitHub exige actuellement une copie sans ajout ni modification
par rapport à la branche distante par défaut : elle ne reprend pas une branche
de fonctionnalité divergente et ne regroupe plus les changements locaux dans un
commit. Pour continuer à travailler sans publication GitHub, retire l'autorisation
avec `codebox bindings revoke github`, puis relance `codebox`. Les fichiers et
l'historique de l'invité sont conservés lors du refus.

**Rafraîchissement d’un environnement déjà préparé :** `workspace sync` transfère
les fichiers filtrés dans l’invité, mais ne les committe plus automatiquement sur
la branche publiable. Cette opération s’arrête avant le `git add` de la copie :
les fichiers transférés restent non commités et récupérables avec `workspace
apply`. Les commits faits par l’agent ne sont pas modifiés.

**Limite de la comparaison :** les fichiers déjà présents sur la branche distante
mais absents de la copie filtrée restent préservés, comme auparavant. Une absence
due au filtre ou à une suppression locale n'est pas interprétée comme une
suppression à publier. La branche préparée n'est donc pas une copie exacte du
dossier hôte. Si l'invité contient des modifications non commitées, la préparation
s'arrête sans les écraser.
L'historique invité antérieur est conservé dans une branche locale
`<branche-invitée>-snapshot-backup` : les commits antérieurs restent accessibles
localement, mais ne sont pas ajoutés à l'historique de la branche publiable.
Seul le contenu déjà présent sur la branche distante est accepté. L’origine et
les marqueurs de préparation sont publiés ensemble. Lors d’un lancement sain
sans changement de configuration, le marqueur de contrôle est vérifié avant les
autres mises à jour invitées, même si le jeton GitHub manque ou que son trousseau
est indisponible. Un changement d’origine approuvée est
refusé avant un redémarrage ; relis et exporte les changements invités avant
de recréer l'environnement.

**Environnements préparés par une ancienne version :** la présence de l'ancien
marqueur GitHub ne suffit pas à reprendre. Sans le nouveau marqueur de contrôle,
codebox refuse la préparation et conserve les références et les fichiers ; il
ne réécrit pas l'historique pour retirer un éventuel commit d'instantané. Retire
l'accès GitHub avec `codebox bindings revoke github` pour reprendre sans
publication. `codebox workspace apply` récupère les modifications non commitées,
mais ni `apply` ni `export` ne récupèrent les changements déjà commités dans
l'invité ([suivi de cette limite : #313](https://github.com/etalab-ia/codebox/issues/313)).
Sauvegarde et relis aussi ces commits avant toute recréation. Cette
protection ne retire pas les fichiers qui auraient déjà été poussés sur GitHub.

La préparation ne publie rien automatiquement. Après revue des changements
dans le guest, l'agent peut pousser sa branche puis ouvrir une PR en brouillon :

```bash
git push -u origin HEAD
gh pr create --draft
```

Pour un jeton fine-grained, accorde `Contents: read and write` et
`Pull requests: read and write` sur le seul dépôt concerné ; une organisation
peut aussi exiger son approbation. Le rôle du compte est vérifié à l'initialisation,
mais GitHub applique aussi les permissions propres au jeton lors du push et de
la création de PR. Le GitHub CLI de l'hôte n'est pas utilisé.

## Importer les secrets applicatifs

L'import commence par une prévisualisation :

```bash
codebox secrets import --profile development --file .env --file apps/api/.env.local
codebox secrets import --profile development --file .env --apply
```

Dans un terminal, l'assistant peut proposer les fichiers dotenv du projet et
des sous-projets ; aucun fichier n'est importé automatiquement. Les chemins
doivent rester dans la racine canonique du projet. Les liens symboliques et
les répertoires usuels de dépendances/build sont exclus de la découverte. En
mode non interactif, indique chaque `--file`. L'aperçu masque les valeurs et
signale les clés répétées ; la dernière définition prévaut. `--apply` demande
confirmation dans un terminal ; un script doit utiliser `--yes` explicitement.

Le périmètre est **checkout projet → profil → fichier/composant → variable**.
Deux projets, profils ou fichiers ayant le même nom de variable restent
distincts. Les aliases symboliques de la même racine convergent ; deux clones
différents ne partagent pas leurs secrets implicitement. Le manifeste projet
ne reçoit ni valeurs ni références locales. Les fichiers sources ne sont pas
modifiés.

Le parseur utilise le dialecte `go-rotini` sans expansion : `$VAR`, `${VAR}` et
`$(...)` restent des valeurs littérales ; aucune variable d'environnement
n'est lue et aucune commande exécutée. Les affectations `KEY=value` et la forme
YAML `KEY: value` sont acceptées, avec le préfixe `export` et des valeurs
entre guillemets doubles ou simples ; les fins de ligne LF, CRLF et CR isolé
séparent les affectations, et un BOM UTF-8 en tête de fichier est accepté.
Un refus nomme la variable et la règle en cause dès que la variable peut être
identifiée et répétée sans risque : un retour chariot isolé au milieu d'une
valeur, une valeur entre backticks, ou un nom de variable hors lettres,
chiffres et tiret bas (chiffre initial interdit, 128 caractères au plus).
Quand le nom ne peut pas être répété sans risque — trop long, ou susceptible
d'être du texte collé plutôt qu'un nom — ou quand la faute n'appartient à
aucune variable identifiable, le refus nomme la règle seule ou reste générique ;
aucun refus ne cite jamais une valeur. La taille d'un fichier est plafonnée à
1 MiB ; chaque valeur à 951 octets UTF-8 pour que son enveloppe base64, encodée
en UTF-16 par Windows Credential Manager, reste dans sa limite de 2 560 octets.

L'import stocke uniquement les valeurs : il n'approuve aucune destination,
n'active pas de liaison proxy et ne livre aucun fichier au guest. `--fallback`
choisit explicitement le magasin JSON local propriétaire (`0600`), qui n'est
pas chiffré par codebox.

## Exposer un secret importé au guest (`secrets bindings`)

Importer ne donne aucun accès. L'étape séparée qui autorise le proxy à exposer
une valeur est l'approbation de liaison, enregistrée localement à l'hôte :

```bash
codebox secrets profile development
codebox secrets bindings approve development .env API_KEY --destination api.example.com
codebox secrets bindings list
codebox secrets bindings revoke development .env API_KEY
```

`--destination` est répétable. Approuver une destination **est** la décision de
sécurité : c'est ce qui autorise la substitution de la valeur vers ces hôtes et
nulle part ailleurs. L'approbation vit dans l'état local de l'hôte, jamais dans
le dépôt, donc elle ne suit pas un clone. Une même destination peut être
partagée par plusieurs liaisons : la qualification native montre que
l'autorisation reste propre à chaque liaison.

La variable invitée est dérivée du composant et du nom (`JC_API_KEY` pour
`.env`, `JC_APPS_API_API_KEY` pour `apps/api/.env`), sauf `--guest-env`
explicite. Les noms réservés aux liaisons système (`ALBERT_API_KEY`,
`GITHUB_TOKEN`, `CONTEXT7_API_KEY`) sont refusés : deux liaisons ne doivent
jamais se disputer une variable.

Dans le guest, la variable contient un marqueur opaque sans dollar
(`__CODEBOX_SECRET_…__`), jamais la valeur. Le choix de ce marqueur est
qualifié : les chargeurs dotenv usuels suppriment une référence `$MSB_…` non
définie, y compris entre guillemets, alors qu'un marqueur opaque survit aux
modes testés (voir `apps/codebox/tests/integration/project-secrets/`).

Le profil actif est explicite : `secrets profile <name>` sélectionne le seul
profil livré, et `--clear` est refusé tant que des liaisons restent approuvées,
car cet état ferait échouer chaque démarrage. Une liaison approuvée dont la
valeur importée a disparu, ou dont le magasin est verrouillé, **bloque le
démarrage** au lieu d'être ignorée : omettre une liaison la transformerait en
état désiré, et le runtime retirerait alors une inscription active.

Retirer une approbation (`revoke`) supprime l'accès du guest, pas la valeur
importée ; l'inscription proxy est retirée à la prochaine application, et le
guest conserve un marqueur inerte jusqu'au redémarrage.

## Secrets lisibles dans le guest (`--delivery guest`)

Certaines valeurs ne peuvent pas passer par le proxy réseau, parce que
l'application les consomme localement : un mot de passe de base de données, une
clé de signature. Il n'y a alors aucune requête sortante à réécrire, donc la
valeur réelle doit exister dans l'invité. C'est un mode **distinct**, approuvé
séparément :

```bash
codebox secrets bindings approve development .env DB_PASSWORD --delivery guest
```

Ce mode n'est pas le mode protégé, et l'approbation affiche l'avertissement
correspondant :

- tout ce qui tourne dans le sandbox, **y compris l'agent**, peut lire la valeur ;
- elle reste lisible aussi longtemps que le sandbox la détient ;
- un instantané pris pendant qu'elle est livrée la capture, et supprimer le
  fichier ensuite ne nettoie pas cet instantané.

Le mode par défaut (`--delivery proxy`, ou sans option) reste la protection par
proxy : la valeur ne franchit jamais la frontière. Les deux modes sont
exclusifs — une liaison lisible dans l'invité n'a ni destination ni variable
invitée, puisque rien n'est substitué vers un hôte — et il n'existe **aucun
repli automatique** de l'un vers l'autre : une valeur n'est jamais lisible dans
l'invité sans une approbation explicite.

Deux encodages sont utilisés selon ce que la valeur contient. Les guillemets
simples sont la forme par défaut : ils portent exactement les retours à la
ligne, les espaces, `#`, les guillemets doubles et l'accent grave. Ils ne
portent **pas** l'apostrophe : on ne peut pas l'échapper dans une valeur quotée,
si bien que `godotenv` refuse le fichier entier et que `python-dotenv` lit de
travers. Un guillemet **double** en première position ne gêne pas les
guillemets, eux : la forme quotée le porte, seule la forme brute y verrait
l'ouverture d'une valeur quotée. Quand les guillemets ne suffisent pas — deux
antislashs consécutifs, une valeur qui se termine par un antislash, une
apostrophe, ou une apostrophe à côté de l'un de ces cas — la valeur est écrite
**sans guillemets**. Dans une
valeur non quotée, aucun des trois chargeurs ne traite les antislashs : les deux
formes y passent donc verbatim. C'est ce qui rend livrables les chemins Windows
et UNC (`C:\Users\`, `\\serveur\partage\`) et toute valeur terminée par un
séparateur de répertoire que les guillemets altéreraient ou feraient refuser le
fichier entier.

La forme brute a ses propres limites : un `#`, un `$`, un retour à la ligne,
un retour chariot, un guillemet simple ou double **en première position**, une
valeur encadrée par deux accents graves, ou un caractère que les chargeurs
suppriment en début ou en fin de valeur y sont refusés. Les guillemets et
l'accent grave sont **positionnels, pas interdits** : en position intérieure ou
finale ce sont des
octets ordinaires. Une apostrophe intérieure ou finale (`it's`, `O'Brien`)
passe donc brute, car les guillemets ne la portent jamais ; elle n'est refusée
que si la forme brute la refuse aussi — apostrophe en première position, ou
accompagnée d'un autre obstacle brut (`$`, retour à la ligne, retour chariot,
caractère rogné en bord de valeur). Un antislash final ou une paire d'antislashs
n'est **pas** un obstacle, c'est la raison d'être de la forme brute : `it's a\\b`
et `C:\Users\O'Brien\` passent bruts. Le jeu accepté a été mesuré, pas
supposé, contre les trois chargeurs épinglés. Le **non-ASCII est accepté** : une
valeur non quotée n'est jamais décodée, donc `C:\Répertoire\` ou `naïve\`
passent octet pour octet. Seuls restent **refusés** à l'approbation les cas
qu'aucune des deux formes ne porte. Le refus est une **conjonction**, pas une
liste de caractères : il faut que les guillemets ne puissent pas porter la
valeur **et** que la forme brute la refuse aussi. Les guillemets sont mis en
échec par un retour chariot, une apostrophe, un `$`, une paire d'antislashs ou
un antislash final ; la forme brute est refusée par un `$`, un `#`, un retour à
la ligne, un retour chariot, une valeur vide, un guillemet simple ou double en
première position, une valeur encadrée par deux accents graves, ou un caractère
que les chargeurs rognent en bord de valeur. Sont donc refusés, entre autres :
un `$`, un retour chariot, un guillemet double en première position **dans une
valeur que les guillemets ne portent pas déjà**, une apostrophe en première
position (elle fait échouer les guillemets à elle seule), une apostrophe
accompagnée d'un autre obstacle brut, une paire d'antislashs suivie d'un `#` ou
d'un retour à la ligne, une valeur encadrée par deux accents graves quand les
guillemets ne peuvent pas la porter, et une séquence UTF-8 invalide. Le refus
nomme la variable et la raison ; une valeur refusée est un désagrément, une
valeur silencieusement modifiée est un incident qui se produit loin d'ici. Pour
ces valeurs, la livraison par proxy reste la bonne réponse dès lors que
l'application joint le service par le réseau.

Le fichier livré est écrit en `0600`, et la restriction est appliquée au fichier
temporaire **avant** le renommage : le runtime crée les fichiers en `0644`, donc
restreindre après coup laisserait une fenêtre pendant laquelle le secret serait
lisible par tous les comptes de l'invité. Un fichier qui contient une valeur
réelle porte un avertissement en tête, et `codebox secrets bindings list`
distingue les deux modes.

## Fichiers dotenv générés dans le guest

À chaque démarrage, les liaisons approuvées sont aussi écrites dans le
workspace du guest, à l'emplacement de leur composant :

```
/workspace/.env
/workspace/apps/api/.env
```

Chaque fichier ne contient que les marqueurs opaques des variables approuvées
pour ce composant, précédés d'un en-tête `# codebox managed: do not edit`.
Les composants gardent des fichiers distincts : l'ordre de chargement du
framework est donc préservé, et deux sous-projets qui définissent la même
variable ne se mélangent pas. Un composant sans liaison approuvée ne produit
aucun fichier, pour ne pas masquer un fichier fourni par le projet.

codebox n'écrase jamais un fichier qu'il n'a pas écrit. Si un `.env` fourni
par le projet occupe la cible, la publication est **refusée** avec le chemin
concerné, au lieu de détruire votre contenu. Les écritures passent par un
fichier temporaire puis un renommage, et un fichier géré dont l'approbation est
retirée est supprimé au démarrage suivant — la suppression revérifie l'en-tête
au moment d'agir, pour ne jamais effacer un fichier que vous auriez mis à la
place.

L'inventaire de ce que le sandbox contient réellement (variables invitées et
fichiers gérés) est conservé localement à l'hôte, à côté des approbations
(`~/.local/state/codebox/project-secrets/<projet>/applied.json`). Il est relu
à chaque application : c'est ce qui permet à une révocation de désinscrire
effectivement la liaison, y compris après un `restart`.

Ces fichiers ne contiennent jamais de valeur : seulement les mêmes marqueurs que
les variables d'environnement du guest. La valeur réelle reste sur l'hôte et
n'est substituée que par le proxy réseau, uniquement vers les destinations
approuvées. Un client qui signe localement le marqueur reste hors périmètre.

## Révocation

`codebox auth remove` révoque l'accès avant de supprimer l'entrée du magasin : sur les instances Microsandbox en cours d'exécution, la liaison proxy est supprimée à chaud (l'invité garde un placeholder inerte jusqu'au prochain redémarrage — un avertissement le signale) ; sur les instances arrêtées, la référence persistée est retirée pour le prochain démarrage. La révocation raisonne par **entrée de magasin**, pas par nom de liaison : un `credentialRef` peut alimenter la liaison Albert depuis une entrée nommée autrement, et c'est la liaison invitée effectivement alimentée qui est retirée (l'instantané le consigne sous la forme `entrée@magasin#liaison`). Supprimer l'entrée d'un magasin ne touche pas les instances liées à l'autre magasin ; une instance dont la clé venait de l'environnement (variable `ALBERT_API_KEY`) est ignorée, cette variable n'appartenant pas à codebox ; une source non confirmable est révoquée puis signalée. Sur Tart et agent-vm, la clé est lisible en clair dans l'invité : la suppression est **refusée** tant qu'une telle instance tourne — y compris lorsque l'entrée supprimée n'est pas nommée `albert` mais alimente la liaison Albert — car retirer la copie du magasin ne révoquerait rien.

## Profils

Un profil nomme un jeu de valeurs pour un même projet : `development`,
`production`, ou ce que le projet utilise. Le périmètre d'import est
**checkout → profil → fichier/composant → variable** : deux profils ayant la
même variable restent distincts, et un même nom de variable dans deux
composants aussi.

Un seul profil est actif à la fois (`codebox secrets profile <nom>`), et il
n'est jamais deviné : `--clear` est refusé tant que des liaisons restent
approuvées, parce que cet état ferait échouer chaque démarrage. `development` et
`production` ne sont jamais combinés implicitement : une liaison enregistrée
pour un autre profil que l'actif est une erreur, pas un repli.

## Limites de protection

Ce que chaque mode garantit, et ce qu'il ne garantit pas.

**Livraison par proxy (défaut).** La valeur ne franchit jamais la frontière :
l'invité ne détient qu'un marqueur. En revanche, **le guest peut utiliser la
valeur** vers les hôtes approuvés — n'importe quel processus de l'invité, y
compris l'agent, peut appeler ces hôtes et obtenir la substitution. Empêcher la
lecture et autoriser l'usage sont deux choses différentes : approuver une
destination est précisément ce choix.

**Livraison lisible dans l'invité (`--delivery guest`).** Tout ce qui tourne
dans le sandbox, y compris l'agent, peut lire la valeur, et elle le reste tant
que le sandbox la détient. Un instantané pris pendant qu'elle est livrée la
capture : révoquer ensuite ne nettoie pas cet instantané. Ce mode existe pour
les valeurs qu'une application consomme localement ; pour tout ce qui passe par
le réseau, le mode proxy est le bon.

**Sources conservées.** codebox ne modifie ni ne supprime les fichiers `.env`
d'origine : ils restent sur l'hôte, et dans l'historique Git s'ils sont
versionnés. L'espace de travail scellé empêche leur transfert vers l'invité,
mais rien ne les retire du poste. Les nettoyer relève de l'utilisateur.

**Magasin.** Les valeurs sont confiées au magasin natif de la plateforme
(Trousseau, Secret Service, Credential Manager) ; codebox n'ajoute aucun
chiffrement de son côté. Le magasin de repli (`--fallback`) est un fichier JSON
en clair, lisible par le seul propriétaire (`0600`), **non chiffré** : il
demande un consentement explicite et son état est rappelé à chaque affichage.

**Affichage.** Aucune commande codebox n'affiche une valeur : ni l'aperçu
d'import, ni la liste des liaisons, ni `config explain`.

## Différence avec `config import-env`

Les deux commandes importent un fichier `.env`, et rien d'autre ne les
rapproche :

| | `config import-env` | `secrets import` |
| --- | --- | --- |
| Objet | la configuration **de codebox** | les secrets **de l'application** |
| Destination | les champs de configuration gérés | le magasin d'identifiants |
| Suite | rien n'est écrit : prévisualisation seule | stockage, puis approbation séparée pour la livraison |
| Clés d'identifiants | jamais copiées ; signalées vers le magasin | c'est précisément ce qui est importé |
| Fichier source | jamais modifié | jamais modifié |

`config import-env` sert donc à adopter un `.env` hérité qui portait la
configuration de l'outil (`RUNTIME`, `ISOLATION`, chemins). `secrets import`
sert à extraire les variables propres au projet pour que le sandbox puisse les
recevoir. Un même fichier peut alimenter les deux, et c'est le cas courant d'un
`.env` de projet qui contenait aussi des réglages de l'outil.
