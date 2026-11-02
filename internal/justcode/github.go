package justcode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitHubRemote is the credential-free form of a GitHub.com origin.
type GitHubRemote struct {
	URL  string
	Repo string
}

const (
	githubCLIVersion  = "2.100.0"
	githubCLIAMD64SHA = "e4d4bb4498e8d007abe545b6568926793ace1b6447da598294a610018cb164be"
	githubCLIARM64SHA = "ea4e7a581a32ccad6cc7923cb1576ac5859ba4b9a16ab22eb8f8a96e78e2e961"
)

// GitHubOriginForProject reads the host checkout's origin and returns a
// credential-free HTTPS URL. Host credentials embedded in remote URLs are
// discarded; guest authentication is provided separately by the proxy.
func GitHubOriginForProject(ctx context.Context, root string) (GitHubRemote, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "--all", "origin")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return GitHubRemote{}, fmt.Errorf("the project has no readable origin remote; configure a GitHub.com origin to enable the guest GitHub workflow")
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return GitHubRemote{}, fmt.Errorf("the project must have exactly one origin URL to enable the guest GitHub workflow")
	}
	return ParseGitHubRemote(strings.TrimSpace(lines[0]))
}

// ParseGitHubRemote accepts GitHub.com HTTPS, SSH and SCP-style origin URLs,
// but never carries their userinfo into the returned remote.
func ParseGitHubRemote(raw string) (GitHubRemote, error) {
	var path string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	case strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || (u.Port() != "" && u.Port() != "22") || u.RawQuery != "" || u.Fragment != "" {
			return GitHubRemote{}, fmt.Errorf("the origin is not a supported GitHub.com remote")
		}
		path = u.EscapedPath()
	case strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || (u.Port() != "" && u.Port() != "443") || u.RawQuery != "" || u.Fragment != "" {
			return GitHubRemote{}, fmt.Errorf("the origin is not a supported GitHub.com remote")
		}
		path = u.EscapedPath()
	default:
		return GitHubRemote{}, fmt.Errorf("the origin must use HTTPS or SSH on github.com")
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return GitHubRemote{}, fmt.Errorf("the GitHub origin must identify exactly one owner and repository")
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil || !validGitHubOwner(owner) {
		return GitHubRemote{}, fmt.Errorf("the GitHub origin contains an invalid owner")
	}
	repo, err := url.PathUnescape(parts[1])
	if err != nil || !validGitHubRepo(repo) {
		return GitHubRemote{}, fmt.Errorf("the GitHub origin contains an invalid repository name")
	}
	slug := owner + "/" + repo
	return GitHubRemote{URL: "https://github.com/" + slug + ".git", Repo: slug}, nil
}

func validGitHubOwner(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func validGitHubRepo(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// GitHubRepoRights describes what a stored GitHub credential may do on the
// project's origin repository, as far as the init screen needs to tell the
// user. The four failure kinds mirror the distinct causes the wizard must
// surface; Unknown covers an unreachable network or API path.
type GitHubRepoRights int

const (
	GitHubRepoRightsUnknown GitHubRepoRights = iota
	// GitHubRepoRightsOK means the credential can read the repository and
	// push to it, the only state in which the GitHub workflow is offered.
	GitHubRepoRightsOK
	// GitHubRepoRightsNoAccess means the credential cannot read the repository
	// at all (the repository is not granted to a fine-grained token).
	GitHubRepoRightsNoAccess
	// GitHubRepoRightsInsufficient means the credential can read the repository
	// but lacks the push role (Contents / Pull requests write).
	GitHubRepoRightsInsufficient
	// GitHubRepoRightsOrgPending means the repository's organization has not
	// approved the credential yet.
	GitHubRepoRightsOrgPending
	// GitHubRepoRightsSSO means the organization requires SAML single sign-on
	// and the credential is not authorised for it: the remedy is to authorise
	// the token for the organization, not to wait for an approval.
	GitHubRepoRightsSSO
	// GitHubRepoRightsInvalid means GitHub rejected the credential (expired or
	// revoked).
	GitHubRepoRightsInvalid
)

// GitHubProjectStatus classifies a project's GitHub situation for the init
// screen. It carries only non-secret facts: whether a GitHub.com origin
// exists, whether a credential is stored, the account login (never the token
// itself), and what the credential may do on the origin.
type GitHubProjectStatus struct {
	// Remote is the resolved origin. Valid only when HasOrigin is true.
	Remote GitHubRemote
	// HasOrigin is true when the project has a usable GitHub.com origin.
	HasOrigin bool
	// HasToken is true when a GitHub credential is stored on this host.
	HasToken bool
	// TokenError is set when the store could not be read at all (locked,
	// denied, unavailable). It is NOT set when simply no token is stored:
	// that is the ordinary "no token yet" situation, reported by HasToken
	// being false with no error. The distinction matters because the remedy
	// differs — unlock the store, versus add a token.
	TokenError error
	// Login is the account the stored credential authenticates as. Empty when
	// unknown (no token, or GitHub did not report it).
	Login string
	// Rights classifies what the stored credential may do on the origin. It
	// is meaningful only when HasOrigin and HasToken are both true.
	Rights GitHubRepoRights
}

// ProbeGitHubForProject resolves the project origin and stored credential and
// verifies what the credential may do on the origin. It never exposes the
// token. A nil client selects a short default-timeout client. The function
// never fails on a missing origin or a missing credential: those are normal
// situations (the project is not linked, or no token is stored), and they are
// returned as such.
func ProbeGitHubForProject(ctx context.Context, root string, client *http.Client) GitHubProjectStatus {
	return probeGitHubForProjectWith(ctx, root, client, GitHubOriginForProject, ReadStoredCredential)
}

// probeGitHubForProjectWith is ProbeGitHubForProject over injected origin and
// credential readers, so the classification is testable without a Git checkout
// or a real credential store — the same shape resolveAlbertWith uses.
func probeGitHubForProjectWith(
	ctx context.Context,
	root string,
	client *http.Client,
	origin func(context.Context, string) (GitHubRemote, error),
	read func(context.Context, CredentialKind) (string, error),
) GitHubProjectStatus {
	st := GitHubProjectStatus{}
	remote, err := origin(ctx, root)
	if err != nil {
		return st
	}
	st.Remote = remote
	st.HasOrigin = true

	token, err := read(ctx, CredentialGithub)
	if err != nil {
		// A missing credential is the normal "Plus tard" situation; any
		// other error (locked, denied, unavailable) is a real problem the
		// caller must surface instead of offering to add a token.
		if !errors.Is(err, ErrCredentialNotFound) {
			st.TokenError = err
		}
		return st
	}
	st.HasToken = true

	owner, repo := splitGitHubSlug(remote.Repo)
	if owner == "" || repo == "" {
		return st
	}
	p := probeGitHubProjectAt(ctx, client, token, owner, repo, githubUserURL, githubRepoAPIPath(owner, repo), githubPullsAPIPath(owner, repo))
	st.Login = p.login
	st.Rights = p.rights
	return st
}

func splitGitHubSlug(slug string) (string, string) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func githubRepoAPIPath(owner, repo string) string {
	return "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// githubPullsAPIPath is the pull-request listing endpoint. Reading it needs the
// Pull requests permission, while the repository endpoint needs only Metadata
// read, so it is what distinguishes a token that can open the promised draft
// pull request from one that can only push.
func githubPullsAPIPath(owner, repo string) string {
	return githubRepoAPIPath(owner, repo) + "/pulls?per_page=1"
}

type githubProjectProbeResult struct {
	login  string
	rights GitHubRepoRights
}

// probeGitHubProjectAt verifies a token against the GitHub user, repository and
// pull-request endpoints. userURL may be empty to skip the login lookup, and
// pullsURL may be empty to skip the pull-request check. The repository endpoint
// decides most of the rights classification; the user endpoint only enriches
// the result with the account login, so a failed login lookup never turns a
// valid token into a failure.
//
// The pull-request check exists because the push role is not the same thing as
// the Pull requests permission: a fine-grained token can report
// `permissions.push: true` while lacking Pull requests write, and then the
// draft PR the wizard promises is rejected at use time. Only when both hold is
// the credential reported as fully usable.
func probeGitHubProjectAt(ctx context.Context, client *http.Client, token, owner, repo, userURL, repoURL, pullsURL string) githubProjectProbeResult {
	client = noRedirectClient(client)
	res := githubProjectProbeResult{rights: GitHubRepoRightsUnknown}
	if userURL != "" {
		res.login, _ = fetchGitHubLogin(ctx, client, token, userURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL, nil)
	if err != nil {
		return res
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return res
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Permissions struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
			return res
		}
		switch {
		case !body.Permissions.Push:
			res.rights = GitHubRepoRightsInsufficient
		case pullsURL == "":
			res.rights = GitHubRepoRightsOK
		default:
			switch githubPullRequestAccess(ctx, client, token, pullsURL) {
			case pullAccessYes:
				res.rights = GitHubRepoRightsOK
			case pullAccessNo:
				res.rights = GitHubRepoRightsInsufficient
			default:
				// Throttled, unreachable or otherwise unanswerable. Saying
				// "insufficient rights" would send the user to change a token
				// that may be fine, so report that the check could not run.
				res.rights = GitHubRepoRightsUnknown
			}
		}
	case http.StatusUnauthorized:
		res.rights = GitHubRepoRightsInvalid
	case http.StatusNotFound:
		res.rights = GitHubRepoRightsNoAccess
	case http.StatusForbidden:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		lower := strings.ToLower(string(data))
		switch {
		case strings.Contains(lower, "rate limit") || resp.Header.Get("X-RateLimit-Remaining") == "0":
			// A throttled probe says nothing about rights. Reporting it as
			// insufficient rights would send the user to change a token that
			// is fine, and Revérifier would loop until the window resets.
			res.rights = GitHubRepoRightsUnknown
		case strings.Contains(lower, "saml"):
			// SSO enforcement is fixed by authorising the token for the
			// organization, not by waiting for an approval.
			res.rights = GitHubRepoRightsSSO
		case strings.Contains(lower, "organization") || strings.Contains(lower, "approved"):
			res.rights = GitHubRepoRightsOrgPending
		default:
			res.rights = GitHubRepoRightsInsufficient
		}
	default:
		res.rights = GitHubRepoRightsUnknown
	}
	return res
}

func fetchGitHubLogin(ctx context.Context, client *http.Client, token, url string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var raw struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return "", false
	}
	return raw.Login, raw.Login != ""
}

// githubCLIInstallScript installs the pinned, statically linked GitHub CLI in
// the Microsandbox guest only. The release checksums are embedded; no host gh
// executable or host credential helper is used by the guest workflow.
func githubCLIInstallScript() string {
	return fmt.Sprintf(`
just_code_install_gh() (
  set -eu
version=%s
case "$(uname -m)" in
  x86_64|amd64) arch=amd64; digest=%s ;;
  aarch64|arm64) arch=arm64; digest=%s ;;
  *) echo "GitHub CLI %s is unavailable for guest architecture $(uname -m)" >&2; exit 1 ;;
esac
binary=/usr/local/bin/gh
if [ -x "$binary" ] && "$binary" --version 2>/dev/null | grep -q "^gh version $version"; then
  exit 0
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
archive="$tmp/gh.tar.gz"
asset="gh_${version}_linux_${arch}.tar.gz"
curl -fsSL "https://github.com/cli/cli/releases/download/v${version}/${asset}" -o "$archive"
printf '%%s  %%s\n' "$digest" "$archive" | sha256sum -c - >/dev/null
tar -xzf "$archive" -C "$tmp"
mkdir -p /usr/local/bin
cp "$tmp/gh_${version}_linux_${arch}/bin/gh" "$tmp/gh"
chmod 0755 "$tmp/gh"
mv "$tmp/gh" "$binary"
"$binary" --version | grep -q "^gh version $version"
)
just_code_install_gh
`, githubCLIVersion, githubCLIAMD64SHA, githubCLIARM64SHA, githubCLIVersion)
}

// GitHubBranchName creates a unique guest-local work branch so a recreated
// project instance never overwrites an existing remote branch.
func GitHubBranchName(instance string) (string, error) {
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create a unique guest branch name: %w", err)
	}
	name := safeFileName(instance)
	if len(name) > 60 {
		name = name[:60]
	}
	return fmt.Sprintf("just-code/%s-%s-%s", name, time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(nonce[:])), nil
}

// githubSnapshotBranchScript only prepares a publishable branch when the
// filtered snapshot adds no changes to the remote default-branch tree.
// Permission to transfer a file into the guest is not permission to publish it.
func githubSnapshotBranchScript(workspace string, remote GitHubRemote, baseBranch, branch string) string {
	baseRef := "refs/heads/" + baseBranch
	return "set -eu; cd " + shellQuote(workspace) +
		"; if [ -n \"$(git status --porcelain --untracked-files=all)\" ]; then echo 'guest worktree is dirty; export or review its changes before enabling the GitHub workflow' >&2; exit 1; fi" +
		"; if [ -n \"$(git ls-files --others --ignored --exclude-standard)\" ]; then echo 'guest workspace contains ignored files; review them before enabling the GitHub workflow' >&2; exit 1; fi" +
		"; tmp=$(mktemp -d /tmp/just-code-github-index.XXXXXX)" +
		"; temp_config=''; trap 'rm -rf \"$tmp\"; if [ -n \"$temp_config\" ]; then rm -f \"$temp_config\"; fi' EXIT HUP INT TERM" +
		"; git ls-files --stage -z > \"$tmp/snapshot\"" +
		"; git fetch --depth=1 " + shellQuote(remote.URL) + " " + shellQuote(baseRef) +
		"; base=$(git rev-parse FETCH_HEAD)" +
		"; GIT_INDEX_FILE=\"$tmp/index\" git read-tree \"$base\"" +
		"; GIT_INDEX_FILE=\"$tmp/index\" git update-index -z --index-info < \"$tmp/snapshot\"" +
		"; tree=$(GIT_INDEX_FILE=\"$tmp/index\" git write-tree)" +
		"; base_tree=$(git rev-parse \"$base^{tree}\")" +
		"; if [ \"$tree\" != \"$base_tree\" ]; then " +
		"printf '%s\\n' 'GitHub : la copie locale contient des fichiers nouveaux ou modifiés par rapport à la branche distante par défaut. Leur publication n’a pas été approuvée :' >&2; " +
		"git -c core.quotePath=true diff-tree --no-commit-id --name-status -r \"$base_tree\" \"$tree\" >&2; " +
		"printf '%s\\n' 'Aucune branche publiable ni aucun commit automatique n’a été créé. Relisez ces changements sur votre poste et décidez séparément de leur publication.' " +
		"'Pour travailler sans publication GitHub : just-code bindings revoke github. La commande just-code workspace apply récupère les modifications non commitées, pas les commits de l’invité. Sauvegardez aussi ces commits avant toute recréation.' >&2; exit 1; fi" +
		"; config=$(git rev-parse --git-path config); temp_config=$(mktemp \"$config.just-code.XXXXXX\"); cp \"$config\" \"$temp_config\"" +
		"; git config --file \"$temp_config\" remote.origin.url " + shellQuote(remote.URL) +
		"; git config --file \"$temp_config\" remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'" +
		"; git config --file \"$temp_config\" push.autoSetupRemote true" +
		"; git config --file \"$temp_config\" just-code.github-workflow-origin " + shellQuote(remote.URL) +
		"; git config --file \"$temp_config\" just-code.github-workflow-branch " + shellQuote(branch) +
		"; git config --file \"$temp_config\" just-code.github-workflow-snapshot-policy remote-only-v1" +
		"; git branch " + shellQuote(branch+"-snapshot-backup") + " HEAD" +
		"; git branch -m " + shellQuote(branch) +
		"; git reset --hard \"$base\"" +
		"; mv \"$temp_config\" \"$config\""
}

// pullAccess is the answer to "may this token read the repository's pull
// requests", which is the permission needed to open the draft PR the wizard
// offers. reading is not the same as pushing: a fine-grained token can push
// while lacking Pull requests access.
type pullAccess int

const (
	// pullAccessUnknown means the question could not be answered (throttled,
	// unreachable, unexpected status). It must not be reported as a rights
	// problem, because the remedy may be nothing at all.
	pullAccessUnknown pullAccess = iota
	pullAccessYes
	pullAccessNo
)

// githubPullRequestAccess asks the pull-request listing, a read that changes
// nothing on the remote.
func githubPullRequestAccess(ctx context.Context, client *http.Client, token, pullsURL string) pullAccess {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pullsURL, nil)
	if err != nil {
		return pullAccessUnknown
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return pullAccessUnknown
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return pullAccessYes
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		// A 403 here is a permission refusal unless it is a throttle; both
		// endpoints share the rate limit, so check the header first.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return pullAccessUnknown
		}
		return pullAccessNo
	default:
		return pullAccessUnknown
	}
}
