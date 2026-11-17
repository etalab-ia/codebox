#!/usr/bin/env bash
# Ouvre (ou retrouve) l'issue de suivi annonçant une nouvelle release amont du
# runtime Microsandbox, avec les empreintes SHA-256 issues de l'attestation de
# release GitHub vérifiée cryptographiquement.
#
# Appelé par .github/workflows/msb-runtime-watch.yml (étape « Open or update
# the tracking issue »). Le workflow déclencheur ne modifie rien d'autre : ce
# script ne publie aucune release et ne touche pas au code.
#
# Lit dans l'environnement : GH_TOKEN, GH_REPO, SDK_VERSION,
# UPSTREAM_VERSION, UPSTREAM_REPO et EXPECTED_ASSETS.
set -euo pipefail
title="Mise à jour du runtime Microsandbox : v$UPSTREAM_VERSION disponible"
# A newer upstream release must update an existing tracking issue,
# not be silenced by it: match the version-specific title, and edit
# the issue when the detected version has moved on.
existing="$(gh issue list --state open --search "Mise à jour du runtime Microsandbox in:title" --json number,title --jq '.[] | select(.title == "Mise à jour du runtime Microsandbox : v'"$UPSTREAM_VERSION"' disponible") | .number')"
if [ -n "$existing" ]; then
  echo "A tracking issue for v$UPSTREAM_VERSION is already open (#$existing)."
  exit 0
fi
# Verify the upstream release attestation cryptographically and
# source the published digests from its verified subjects. Fails
# closed when the attestation is absent or invalid.
gh release verify "v$UPSTREAM_VERSION" --repo "$UPSTREAM_REPO" \
  --format json > attestation.json
signer="$(jq -r '.verificationResult.signature.certificate.subjectAlternativeName' attestation.json)"
echo "Attestation verified; signer: $signer"
digests=""
for asset in $EXPECTED_ASSETS; do
  digest="$(jq -r --arg a "$asset" '.verificationResult.statement.subject[] | select(.name == $a) | .digest.sha256' attestation.json)"
  if [ -z "$digest" ] || [ "$digest" = "null" ]; then
    echo "::error::$asset is not covered by the upstream release attestation"
    exit 1
  fi
  digests="${digests}${digest}  ${asset}"$'\n'
done
{
  echo "Une nouvelle release amont du runtime Microsandbox est disponible."
  echo ""
  echo "- Version SDK embarquée : \`v$SDK_VERSION\`"
  echo "- Version amont détectée : \`v$UPSTREAM_VERSION\`"
  echo ""
  echo "Empreintes SHA-256 amont des cinq archives, issues de l'attestation de release GitHub vérifiée cryptographiquement (signataire : \`$signer\`) :"
  echo ""
  echo '```'
  printf '%s' "$digests"
  echo '```'
  echo ""
  echo "Procédure :"
  echo "1. Bump du SDK \`github.com/superradcompany/microsandbox/sdk/go\` vers \`v$UPSTREAM_VERSION\` dans \`apps/codebox/go.mod\`."
  echo "2. PR de code mettant à jour ensemble \`msbRuntimeVersion\`, \`msbRuntimeReleaseURL\` et les cinq empreintes dans \`apps/codebox/internal/codebox/msb_runtime_install.go\` (\`TestMSBRuntimeVersionMatchesSDK\` bloque un décalage). \`msbRuntimeReleaseURL\` pointe vers la release amont : \`https://github.com/$UPSTREAM_REPO/releases/download/v$UPSTREAM_VERSION\`."
  echo ""
  echo "👾 Generated with [Letta Code](https://letta.com)"
} > issue-body.md
gh issue create \
  --title "$title" \
  --label enhancement \
  --body-file issue-body.md
