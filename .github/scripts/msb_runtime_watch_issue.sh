#!/usr/bin/env bash
# Ouvre (ou retrouve) l'issue de suivi annonçant une nouvelle release amont du
# runtime Microsandbox, avec les empreintes SHA-256 issues de l'attestation de
# release GitHub vérifiée cryptographiquement.
#
# Appelé par .github/workflows/msb-runtime-watch.yml. Ce script ne publie
# aucune release et ne modifie pas le checkout ; le workflow réutilise son
# attestation vérifiée et son numéro d'issue pour préparer la PR de mise à jour.
#
# Lit dans l'environnement : GH_TOKEN, GH_REPO, SDK_VERSION,
# UPSTREAM_VERSION, UPSTREAM_REPO, EXPECTED_ASSETS et, facultativement,
# MSB_ATTESTATION_PATH, MSB_ISSUE_BODY_PATH et GITHUB_OUTPUT.
set -euo pipefail
title="Mise à jour du runtime Microsandbox : v$UPSTREAM_VERSION disponible"
attestation_path="${MSB_ATTESTATION_PATH:-attestation.json}"
issue_body_path="${MSB_ISSUE_BODY_PATH:-issue-body.md}"
# Always verify the release, even when the tracking issue already exists: the
# same verified subjects feed the automatic update PR. Fail closed rather than
# letting an existing issue suppress validation.
gh release verify "v$UPSTREAM_VERSION" --repo "$UPSTREAM_REPO" \
  --format json > "$attestation_path"
signer="$(jq -r '.verificationResult.signature.certificate.subjectAlternativeName' "$attestation_path")"
echo "Attestation verified; signer: $signer"
digests=""
for asset in $EXPECTED_ASSETS; do
  digest="$(jq -r --arg a "$asset" '.verificationResult.statement.subject[] | select(.name == $a) | .digest.sha256' "$attestation_path")"
  if [ -z "$digest" ] || [ "$digest" = "null" ]; then
    echo "::error::$asset is not covered by the upstream release attestation"
    exit 1
  fi
  digests="${digests}${digest}  ${asset}"$'\n'
done

# Match the version-specific title so a newer release is not silenced by an
# older tracking issue. Export the issue number for the draft PR body.
existing="$(gh issue list --state open --search "Mise à jour du runtime Microsandbox in:title" --json number,title --jq '.[] | select(.title == "Mise à jour du runtime Microsandbox : v'"$UPSTREAM_VERSION"' disponible") | .number')"
if [ -n "$existing" ]; then
  if [[ ! "$existing" =~ ^[0-9]+$ ]]; then
    echo "::error::Expected exactly one open tracking issue for v$UPSTREAM_VERSION, got: $existing"
    exit 1
  fi
  echo "A tracking issue for v$UPSTREAM_VERSION is already open (#$existing)."
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'issue_number=%s\n' "$existing" >> "$GITHUB_OUTPUT"
  fi
  exit 0
fi
{
  echo "A new upstream Microsandbox runtime release is available."
  echo ""
  echo "- Embedded SDK version: \`v$SDK_VERSION\`"
  echo "- Detected upstream version: \`v$UPSTREAM_VERSION\`"
  echo ""
  echo "SHA-256 digests for the five archives, sourced from the cryptographically verified GitHub release attestation (signer: \`$signer\`):"
  echo ""
  echo '```'
  printf '%s' "$digests"
  echo '```'
  echo ""
  echo "Automation and review:"
  echo "1. The watcher prepares one signed draft PR that updates the SDK, runtime URL, five digests, native-kit pins, and documentation together."
  echo "2. Marking the draft ready for review starts the required hosted CI and Codex review. Merging remains manual."
  echo "3. Paid Scaleway hardware qualification is only run through explicit manual dispatch; this watcher never provisions infrastructure."
  echo ""
  echo "👾 Generated with [Letta Code](https://letta.com)"
} > "$issue_body_path"
issue_url="$(gh issue create \
  --title "$title" \
  --label enhancement \
  --body-file "$issue_body_path")"
issue_number="${issue_url##*/}"
if [[ ! "$issue_number" =~ ^[0-9]+$ ]]; then
  echo "::error::Could not parse the created tracking issue number from: $issue_url"
  exit 1
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  printf 'issue_number=%s\n' "$issue_number" >> "$GITHUB_OUTPUT"
fi
printf '%s\n' "$issue_url"
