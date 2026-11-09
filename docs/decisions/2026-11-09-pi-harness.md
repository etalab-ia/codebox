# Pi comme second harnais (#176)

Date : 2026-11-09. Issue : [#176](https://github.com/etalab-ia/just-code/issues/176).

## Ce qui a changé depuis le report du 27 octobre

Le 27 octobre 2026, #176 avait été reporté sur deux constats et une dépendance :
Pi 0.73.1 n'envoie jamais `tool_choice`, sans quoi l'API Albert renvoyait
`tool_calls: []` et le modèle écrivait l'appel d'outil en texte ; Pi n'a pas de
MCP intégré ; on attendait qu'OpenCode 2.0 soit disponible.

OpenCode v2 est arrivé et just-code l'épingle déjà (`@opencode/cli@2.2.5`,
`OpenCodeVersion`). Pi, lui, n'a **pas bougé** : la dernière version publiée de
`@mariozechner/pi-coding-agent` reste 0.73.1 (7 mai 2026), elle ne contient
toujours aucune occurrence de `tool_choice`, et sa documentation continue
d'exclure MCP par choix de conception.

## Mesures du 9 novembre 2026

Toutes les mesures ont été faites depuis un sandbox Linux, avec la clé Albert
de production, sur `https://albert.api.etalab.gouv.fr/v1/chat/completions`.

**Sans `tool_choice`, en flux** (5 appels par modèle, même invite, `tools`
présent, aucun `tool_choice`) :

| Modèle | Appels d'outils structurés |
|---|---|
| `deepseek-ai/DeepSeek-V4-Flash-0731` | 5/5 |
| `openai/gpt-oss-120b` | 5/5 |
| `ministral-3-8b-instruct-2512` | 5/5 |
| `Qwen/Qwen3-Coder-30B-A3B-Instruct` | 3/5 (0/6 lors d'une autre série, 1/3 lors d'une troisième) |

**Sans flux** (hors flux), le même `qwen3-coder` répond 6/6 en appels
structurés : la défaillance est propre au chemin en flux. Ajouter
`tool_choice: "auto"` ne la corrige pas (3/6 en flux).

**De bout en bout avec Pi** (`pi -p`, invite réelle du harnais, `models.json`
déclaratif, aucun intermédiaire) : `gpt-oss-120b` exécute réellement l'outil
`bash` 4/4 ; `qwen3-coder` échoue 6/6, le modèle écrivant
`<function=bash>…` en texte.

**Avec OpenCode lui-même** : une exécution `opencode run` configurée comme
just-code (`qwen3-coder`, 12 outils, aucun `tool_choice`, en flux) échoue de la
même façon — l'appel d'outil arrive en texte et rien ne s'exécute. Le même
essai avec `deepseek-v4-flash` exécute l'outil.

Conclusion : la défaillance du 27 octobre n'était pas « Pi » mais « le chemin en
flux d'Albert pour certains modèles », et elle n'est pas propre à Pi. Pour les
modèles qui produisent des appels structurés en flux, Pi fonctionne sans shim,
y compris pour l'exécution d'outils.

## Ce que ce lot implémente

- `harness` dans le manifeste projet (`opencode` par défaut, `pi`), relu au
  lancement ; `JUST_CODE_HARNESS` prioritaire. Le champ change de sens pour un
  binaire antérieur, donc le manifeste passe en `schemaVersion` 6 : un binaire
  qui ne connaît pas le harnais refuse le projet au lieu d'exécuter OpenCode.
- Pi exige l'isolation complète (pas de mode serveur) : l'assistant et le
  lancement refusent `pi` + isolation partielle.
- Installation épinglée (`@mariozechner/pi-coding-agent@0.73.1`) dans l'invité,
  configuration écrite dans `~/.pi/agent` depuis le même overlay que celui
  d'OpenCode (Albert et plateformes non-Albert), skills de projet dans
  `~/.pi/agent/skills`, reprise de session via `pi --continue`.
- Les connecteurs MCP ne s'appliquent qu'avec OpenCode : pour Pi, ils ne
  sélectionnent plus l'image navigateur et ne sont pas transmis.
- La porte de confiance couvre la surface Pi, sur tous les chemins de
  lancement : `.pi/settings.json`, les fichiers sous `.pi/extensions/`, et les
  fichiers locaux que les entrées de `extensions` désignent (résolues comme Pi
  les résout, relativement à `.pi`). Chaque fichier est approuvé par son propre
  contenu, donc une modification tirée après approbation redemande une décision.
  Un motif glob est élargi à son répertoire parent littéral : la liste peut
  contenir des fichiers que le motif ne charge pas, jamais en omettre un. Une
  déclaration impossible à épingler par contenu (motif à la racine du projet,
  chemin hors du projet, chemin relatif au domicile) fait échouer le lancement
  au lieu d'être ignorée.

## Limites connues, à traiter séparément

- **MCP** : Pi n'en a pas. La question d'une extension maison (pont MCP) est
  ouverte ; en attendant, un projet Pi ignore les connecteurs sélectionnés.
- **Qualification invitée** : ce lot est couvert par des tests unitaires ; les
  trois runtimes (Microsandbox, Tart, agent-vm) n'ont pas été rejoués dans un
  invité réel, faute de `/dev/kvm` et de `limactl` utilisable dans le sandbox de
  développement. La qualification invité reste à produire sur une machine de
  l'équipe avant la sortie beta.
- **Fiabilité des appels d'outils en flux** : elle dépend du modèle côté Albert.
  Ce lot ne la contourne pas et ne restreint pas la liste des modèles ; la
  documentation signale les modèles mesurés fiables et le cas de `qwen3-coder`.
