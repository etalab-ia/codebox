# Harnais d'intégration identifiants Microsandbox (P02)

Rejoue la matrice de caractérisation du transport des identifiants
(décision `docs/decisions/2026-09-24-microsandbox-credential-transport.md`)
contre un vrai runtime Microsandbox épinglé, sur macOS (libkrun).

## Usage

```sh
# vérification de version uniquement — sans laboratoire
tests/integration/credentials/run.sh

# cas réseau (substitution, violations, rotation) — opt-in, laboratoire requis
MSB_CREDENTIAL_INTEGRATION_NETWORK=1 tests/integration/credentials/run.sh
```

Le harnais crée une sandbox jetable `p02-cred-harness` (image `alpine`),
exécute les cas T1-T11 du décision record, puis la supprime. Un échec
d'assertion sort non-zéro avec le nom du cas. Un échec de **préparation**
(laboratoire incomplet, `msb` absent, IP LAN indisponible) sort avec le
code 2 et un message distinct : il n'est jamais compté comme un échec de
transport.

## Laboratoire local

Les cas réseau utilisent les serveurs versionnés dans `lab/` et une CA
avec ses certificats sous `$MSB_CREDENTIAL_LAB` (défaut `/tmp/p02-lab`) :

- `ca.crt` / `ca.key` : CA d'interception TLS (aussi utilisée en amont) ;
- `srv.crt` / `srv.key` : certificat serveur avec SANs `DNS:p02lab.test`,
  `DNS:evil.test`, `IP:<IP-LAN-hôte>`, `IP:172.16.0.5`, `IP:127.0.0.1` ;
- `lab/server.py` (dans le dépôt) : écho HTTPS sur `0.0.0.0:8443`, journalise chaque requête en
  JSON (`{method, path, headers, body}`) dans `requests.jsonl`, répond 302 vers
  `https://evil.test:8443/redirect-target` sur `/redirect-to-evil` ;
- `lab/dns.py` (dans le dépôt) : DNS UDP sur `0.0.0.0:5354`, répond `p02lab.test` et
  `evil.test` -> IP LAN hôte, NODATA pour leurs requêtes AAAA, et REFUSED
  (rcode 5) pour les autres noms afin que le résolveur bascule sur le fallback (1.1.1.1).

L'IP LAN de l'hôte est lue sur `en0`. Le moteur Microsandbox doit résoudre
lui-même les noms autorisés (les entrées `/etc/hosts` invité ne suffisent
pas), d'où le serveur DNS dédié.

Depuis la racine du dépôt, préparer les certificats et démarrer uniquement
le serveur DNS versionné :

```sh
REPO="$PWD"
LAB="${MSB_CREDENTIAL_LAB:-/tmp/p02-lab}"
HOST_IP="$(ipconfig getifaddr en0)"
mkdir -p "$LAB"
(cd "$LAB" && sh "$REPO/tests/integration/credentials/lab/setup.sh" "$HOST_IP")
python3 "$REPO/tests/integration/credentials/lab/dns.py" "$HOST_IP" > "$LAB/dns.log" 2>&1 &
DNS_PID=$!
MSB_CREDENTIAL_LAB="$LAB" MSB_CREDENTIAL_INTEGRATION_NETWORK=1 tests/integration/credentials/run.sh
kill "$DNS_PID"
```

Le harnais lance lui-même `lab/server.py` depuis le dépôt et arrête son
processus à la sortie. Le port 8443 doit être libre ; un ancien serveur
doit être arrêté explicitement. Après acquisition du port et chargement
des certificats, le serveur réinitialise `requests.jsonl` avant de servir
la moindre requête. Les diagnostics vont dans `server.log`, jamais dans
le journal JSON. Les serveurs copiés précédemment dans `/tmp/p02-lab`
ne sont pas utilisés ; `MSB_CREDENTIAL_LAB_RESTART` n'est plus utilisé.

Les tests DNS (réponse NODATA pour AAAA connu, REFUSED pour nom inconnu) et
du cycle de vie du journal TLS sont exécutables sans runtime Microsandbox :

```sh
python3 -m unittest discover -s tests/integration/credentials/lab -p 'test_*.py' -v
```

## Épinglage

Les versions testées sont épinglées dans `pin.json` (CLI msb, SDK Go, image
invité). Le harnais échoue si la version CLI ne correspond pas. Pour
caractériser une CLI candidate sans modifier le pin du SDK Go, définir
`MSB_CLI_VERSION_OVERRIDE` et pointer `MSB_BIN` vers le binaire candidat ; cet
override ne change ni `go.mod` ni le runtime embarqué.

Les query params ne sont pas substitués ; depuis msb 0.7.5, un placeholder
dans cette zone peut être transmis littéralement à un hôte autorisé. T6
vérifie que la valeur secrète n'est pas transmise et que l'en-tête continue
d'être substitué. La matrice doit être reproduite et le décision record mis
à jour avant toute montée de version du runtime.

Exemple pour tester un runtime candidat déjà installé :

```sh
MSB_BIN=/path/to/msb-0.7.8 MSB_CLI_VERSION_OVERRIDE=0.7.8 \
  MSB_CREDENTIAL_INTEGRATION_NETWORK=1 tests/integration/credentials/run.sh
```

## Cas non couverts par le harnais

- Le test d'écriture Git HTTPS réel (clone + push sur dépôt privé jetable)
  et les appels Albert réels ont été menés manuellement dans la session
  P02 ; ils exigent des identifiants réels et ne sont pas rejouables en CI.
  Voir le décision record, section Méthode.
- Le canal body (substitution désactivée par défaut) et le HTTP non-TLS ne
  sont pas rejoués : le premier suit le même chemin que query (violation),
  le second est hors du modèle `require_tls_identity`.
