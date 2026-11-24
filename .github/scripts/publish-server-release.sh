#!/usr/bin/env bash
# Publish a draft codebox-server GitHub release without letting it claim the
# repository's "Latest" release badge (which install.sh and install.ps1 resolve
# to install the codebox CLI).
#
# GitHub's Releases API defaults make_latest to "true" on creation, and a later
# PATCH with make_latest="false" does not demote a release that already holds
# "Latest". Creating codebox-server releases as drafts avoids the initial
# hijack; this script publishes the draft with --latest=false and then verifies
# repos/$GH_REPO/releases/latest, restoring the newest published CLI release
# (carrying SHA256SUMS) if Latest ever drifted.
set -euo pipefail

: "${GH_TOKEN:?GitHub token is required}"
: "${GH_REPO:?GitHub repository is required}"
: "${TAG:?codebox-server release tag is required}"

SERVER_TAG_REGEX='^codebox-server-v?[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$'
CLI_TAG_REGEX='^codebox-v?[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$'

if ! [[ "$TAG" =~ $SERVER_TAG_REGEX ]]; then
  printf 'Expected a codebox-server release tag, got: %s\n' "$TAG" >&2
  exit 2
fi

fetch_latest_tag() {
  gh api "repos/$GH_REPO/releases/latest" | jq -r '.tag_name // empty'
}

find_newest_cli_release() {
  gh api --paginate "repos/$GH_REPO/releases?per_page=100" | jq -rs '
    (add // [])
    | map(
        select(
          .draft == false
          and .prerelease == false
          and ((.assets // []) | any(.name == "SHA256SUMS"))
          and ((.tag_name // "") | test("^codebox-v?[0-9]+\\.[0-9]+\\.[0-9]+([.-][A-Za-z0-9.-]+)?$"))
        )
        | . + {
            _sort_key: (
              .tag_name
              | capture("^codebox-v?(?<maj>[0-9]+)\\.(?<min>[0-9]+)\\.(?<pat>[0-9]+)(?<rest>[.-][A-Za-z0-9.-]+)?$")
              | [
                  (if (.rest // "") == "" then 1 else 0 end),
                  (.maj | tonumber),
                  (.min | tonumber),
                  (.pat | tonumber),
                  (.rest // "")
                ]
            )
          }
      )
    | sort_by([._sort_key, (.published_at // "")])
    | last
    | .tag_name // empty
  '
}

gh release edit "$TAG" --repo "$GH_REPO" --draft=false --latest=false

latest_tag=$(fetch_latest_tag)
if [[ "$latest_tag" =~ $CLI_TAG_REGEX ]]; then
  printf 'Published %s; Latest release remains %s.\n' "$TAG" "$latest_tag"
  exit 0
fi

printf 'Latest release points to non-CLI tag "%s" after publishing %s; restoring newest published CLI release.\n' "$latest_tag" "$TAG" >&2

cli_tag=$(find_newest_cli_release)
if [ -z "$cli_tag" ]; then
  printf 'No published codebox CLI release with SHA256SUMS found to restore Latest (currently "%s").\n' "$latest_tag" >&2
  exit 1
fi

gh release edit "$cli_tag" --repo "$GH_REPO" --latest

verified_latest=$(fetch_latest_tag)
if [ "$verified_latest" != "$cli_tag" ] || ! [[ "$verified_latest" =~ $CLI_TAG_REGEX ]]; then
  printf 'Failed to restore Latest to %s; GitHub still reports "%s".\n' "$cli_tag" "$verified_latest" >&2
  exit 1
fi

printf 'Published %s; restored Latest release to %s.\n' "$TAG" "$verified_latest"
