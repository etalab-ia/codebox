#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd "$(dirname "$0")/../.." && pwd)
installer="$repo_root/scripts/install.sh"
temp_root=$(mktemp -d "${TMPDIR:-/tmp}/codebox-installer-test.XXXXXX")
trap 'rm -rf "$temp_root"' 0

fake_bin="$temp_root/fake bin"
mkdir -p "$fake_bin"
real_mv=$(command -v mv)

cat > "$fake_bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
	-s) printf '%s\n' "${FAKE_OS:-Linux}" ;;
	-m) printf '%s\n' "${FAKE_ARCH:-x86_64}" ;;
	*) exit 2 ;;
esac
EOF

cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=''
url=''
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o|--output) output=$2; shift 2 ;;
		-w|--write-out) shift 2 ;;
		-*) shift ;;
		*) url=$1; shift ;;
	esac
done
printf '%s\n' "$url" >> "$FAKE_URL_LOG"
case "$url" in
	*/releases/latest)
		printf '%s' "$FAKE_RELEASE_URL"
		;;
	*/SHA256SUMS)
		cp "$FAKE_SUMS" "$output"
		;;
	*/"$FAKE_ASSET")
		if [ "${FAKE_FAIL_DOWNLOAD:-0}" = 1 ]; then
			printf 'partial' > "$output"
			exit 22
		fi
		cp "$FAKE_BINARY" "$output"
		;;
	*)
		printf 'unexpected URL: %s\n' "$url" >&2
		exit 22
		;;
esac
EOF

cat > "$fake_bin/mv" <<'EOF'
#!/bin/sh
set -eu
destination=''
for arg do destination=$arg; done
if [ "${FAKE_FAIL_REPLACE:-0}" = 1 ] && [ "$destination" = "$FAKE_TARGET" ]; then
	exit 1
fi
exec "$REAL_MV" "$@"
EOF
chmod +x "$fake_bin/uname" "$fake_bin/curl" "$fake_bin/mv"

fake_binary="$temp_root/fake codebox"
cat > "$fake_binary" <<'EOF'
#!/bin/sh
[ "${1:-}" = setup ] || exit 7
printf called > "$FAKE_SETUP_LOG"
exit "${FAKE_SETUP_EXIT:-0}"
EOF
asset='codebox-linux-x64'
release_tag='codebox-v9.8.7'
release_url="https://github.com/etalab-ia/codebox/releases/tag/$release_tag"
if command -v sha256sum >/dev/null 2>&1; then
	expected=$(sha256sum "$fake_binary" | awk '{print $1}')
else
	expected=$(shasum -a 256 "$fake_binary" | awk '{print $1}')
fi
good_sums="$temp_root/good SHA256SUMS"
printf '%064d  codebox-windows-x64.exe\n%s  codebox-darwin-arm64\n%s  codebox-linux-arm64\n%s  %s\n' \
	0 "$expected" "$expected" "$expected" "$asset" > "$good_sums"
bad_sums="$temp_root/bad SHA256SUMS"
printf '%064d  %s\n' 0 "$asset" > "$bad_sums"

run_installer() {
	home_dir=$1
	sums_file=$2
	fail_download=$3
	fail_replace=$4
	setup_exit=$5
	platform_os=${6:-Linux}
	platform_arch=${7:-x86_64}
	asset_name=${8:-$asset}
	FAKE_OS=$platform_os \
	FAKE_ARCH=$platform_arch \
	FAKE_ASSET=$asset_name \
	FAKE_BINARY="$fake_binary" \
	FAKE_SUMS="$sums_file" \
	FAKE_URL_LOG="$temp_root/urls" \
	FAKE_SETUP_LOG="$temp_root/setup-called" \
	FAKE_RELEASE_URL="$release_url" \
	FAKE_FAIL_DOWNLOAD=$fail_download \
	FAKE_FAIL_REPLACE=$fail_replace \
	FAKE_SETUP_EXIT=$setup_exit \
	FAKE_TARGET="$home_dir/.local/bin/codebox" \
	REAL_MV="$real_mv" \
	HOME="$home_dir" PATH="$fake_bin:$PATH" \
		"$installer"
}

home="$temp_root/home with spaces"
mkdir -p "$home/.local/bin"
printf 'old version\n' > "$home/.local/bin/codebox"
run_installer "$home" "$good_sums" 0 0 0 > "$temp_root/success.out"
cmp -s "$home/.local/bin/codebox" "$fake_binary" || { printf 'new binary was not installed\n' >&2; exit 1; }
[ "$(cat "$home/.local/bin/codebox.previous")" = 'old version' ] || {
	printf 'prior binary was not retained\n' >&2
	exit 1
}
[ -f "$temp_root/setup-called" ] || { printf 'setup was not invoked\n' >&2; exit 1; }
grep -F "$release_tag" "$temp_root/urls" >/dev/null || { printf 'downloads were not pinned to one release\n' >&2; exit 1; }

# The install result must be announced before the setup wizard runs, in French,
# with the resolved version and platform.
success_out="$temp_root/success.out"
grep -F 'Installation de codebox' "$success_out" >/dev/null || { printf 'installer header is not in French\n' >&2; exit 1; }
grep -F '✓ Version 9.8.7 téléchargée (Linux x64)' "$success_out" >/dev/null || { printf 'version/platform line missing or wrong\n' >&2; exit 1; }
grep -F '✓ Intégrité vérifiée' "$success_out" >/dev/null || { printf 'integrity line missing\n' >&2; exit 1; }
grep -F '✓ Installé dans ~/.local/bin/codebox' "$success_out" >/dev/null || { printf 'installed-path line missing\n' >&2; exit 1; }
grep -F 'Lancement de la configuration' "$success_out" >/dev/null || { printf 'setup launch line missing\n' >&2; exit 1; }
install_line=$(grep -n 'Installé dans ~/.local/bin/codebox' "$success_out" | cut -d: -f1)
launch_line=$(grep -n 'Lancement de la configuration' "$success_out" | cut -d: -f1)
[ "$install_line" -lt "$launch_line" ] || { printf 'install result did not appear before setup\n' >&2; exit 1; }

mac_home="$temp_root/mac home"
mkdir -p "$mac_home"
run_installer "$mac_home" "$good_sums" 0 0 0 Darwin arm64 codebox-darwin-arm64 > "$temp_root/mac.out"
cmp -s "$mac_home/.local/bin/codebox" "$fake_binary" || { printf 'macOS arm64 asset mapping failed\n' >&2; exit 1; }

arm_home="$temp_root/linux arm home"
mkdir -p "$arm_home"
run_installer "$arm_home" "$good_sums" 0 0 0 Linux aarch64 codebox-linux-arm64 > "$temp_root/arm.out"
cmp -s "$arm_home/.local/bin/codebox" "$fake_binary" || { printf 'Linux arm64 asset mapping failed\n' >&2; exit 1; }

target="$home/.local/bin/codebox"
printf 'keep me\n' > "$target"
cp "$target" "$temp_root/expected-target"
if run_installer "$home" "$bad_sums" 0 0 0 > "$temp_root/mismatch.out" 2>&1; then
	printf 'checksum mismatch unexpectedly succeeded\n' >&2
	exit 1
fi
cmp -s "$target" "$temp_root/expected-target" || { printf 'checksum failure changed the installed binary\n' >&2; exit 1; }

if run_installer "$home" "$good_sums" 1 0 0 > "$temp_root/download.out" 2>&1; then
	printf 'partial download unexpectedly succeeded\n' >&2
	exit 1
fi
cmp -s "$target" "$temp_root/expected-target" || { printf 'download failure changed the installed binary\n' >&2; exit 1; }

if run_installer "$home" "$good_sums" 0 1 0 > "$temp_root/replace.out" 2>&1; then
	printf 'interrupted replacement unexpectedly succeeded\n' >&2
	exit 1
fi
cmp -s "$target" "$temp_root/expected-target" || { printf 'failed replacement changed the installed binary\n' >&2; exit 1; }
cmp -s "$home/.local/bin/codebox.previous" "$temp_root/expected-target" || { printf 'rollback copy missing after failed replacement\n' >&2; exit 1; }

if run_installer "$home" "$good_sums" 0 0 9 > "$temp_root/setup-failure.out" 2>&1; then
	printf 'setup failure unexpectedly succeeded\n' >&2
	exit 1
fi
cmp -s "$target" "$fake_binary" || { printf 'setup failure rolled back the new binary unexpectedly\n' >&2; exit 1; }
[ "$(cat "$home/.local/bin/codebox.previous")" = 'keep me' ] || { printf 'setup failure did not retain the previous binary\n' >&2; exit 1; }
# The rollback command must use unquoted absolute paths so a copied line works
# (a quoted ~ would not expand in most shells).
# Quoted, so the line survives a HOME containing spaces (this test's $home has
# one); a bare absolute path would split at the space when pasted.
grep -F 'Pour revenir à la version précédente : mv "$HOME/.local/bin/codebox.previous" "$HOME/.local/bin/codebox"' "$temp_root/setup-failure.out" >/dev/null || { printf 'setup failure omitted a usable rollback command\n' >&2; exit 1; }

# No interactive terminal: the installer must NOT edit a shell profile file on
# its own, must say how to get PATH in the current shell, and must still launch
# the setup wizard (with its stdin reattached to the terminal).
if command -v setsid >/dev/null 2>&1; then
	detached_home="$temp_root/detached home"
	mkdir -p "$detached_home"
	: > "$temp_root/detached-urls"
	set +e
	setsid env FAKE_OS=Linux FAKE_ARCH=x86_64 FAKE_ASSET=$asset FAKE_BINARY="$fake_binary" \
		FAKE_SUMS="$good_sums" FAKE_URL_LOG="$temp_root/detached-urls" \
		FAKE_RELEASE_URL="$release_url" FAKE_SETUP_LOG="$temp_root/detached-setup-called" \
		FAKE_TARGET="$detached_home/.local/bin/codebox" REAL_MV="$real_mv" \
		SHELL=/bin/zsh HOME="$detached_home" PATH="$fake_bin:$PATH" \
		"$installer" > "$temp_root/detached.out" 2>&1 < /dev/null
	detached_status=$?
	set -e
	[ "$detached_status" -eq 0 ] || { printf 'headless install did not complete (exit %s)\n' "$detached_status" >&2; exit 1; }
	[ -f "$temp_root/detached-setup-called" ] || { printf 'headless install did not launch setup\n' >&2; exit 1; }
	for profile in .zshenv .bash_profile .profile .zprofile .bashrc; do
		[ ! -e "$detached_home/$profile" ] || { printf 'headless install edited ~/%s without asking\n' "$profile" >&2; exit 1; }
	done
	grep -F 'ne serait pas trouvée dans un nouveau terminal' "$temp_root/detached.out" >/dev/null || { printf 'headless install did not warn about PATH\n' >&2; exit 1; }
	grep -F 'export PATH="$HOME/.local/bin:$PATH"' "$temp_root/detached.out" >/dev/null || { printf 'headless install did not print the PATH line\n' >&2; exit 1; }
fi

	# Linux never targets a login file: a new terminal there is a non-login
	# bash, which reads only ~/.bashrc. A ~/.profile present alongside must be
	# left alone (and no ~/.bash_profile may be created to shadow it).
	bash_home="$temp_root/bash home"
	mkdir -p "$bash_home"
	printf 'export EDITOR=vi\n' > "$bash_home/.profile"
	# No terminal means nothing is written, so drive the interactive path
	# through a pty: that is where the prompt names the target file.
	python3 - "$bash_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/bash-profile.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/bash", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")  # accept the PATH change
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -F 'Ajouter la ligne suivante à ~/.bashrc ?' "$temp_root/bash-profile.out" >/dev/null || { printf 'linux bash must target ~/.bashrc even when ~/.profile exists\n' >&2; exit 1; }
	[ ! -e "$bash_home/.bash_profile" ] || { printf 'installer created a shadowing ~/.bash_profile\n' >&2; exit 1; }
	grep -F 'export PATH="$HOME/.local/bin:$PATH"' "$bash_home/.bashrc" >/dev/null || { printf 'PATH line not written to ~/.bashrc\n' >&2; exit 1; }
	grep -F 'export EDITOR=vi' "$bash_home/.profile" >/dev/null || { printf 'existing ~/.profile content was destroyed\n' >&2; exit 1; }

	# Empty SHELL: unknown, must fall back to ~/.profile, not ~/.zshenv.
	empty_home="$temp_root/empty shell home"
	mkdir -p "$empty_home"
	python3 - "$empty_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/empty-shell.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	# An unknown shell gets the line to run, not a profile edit: there is no
	# startup file we can be sure it reads, and claiming one was edited would be
	# false for fish/nushell and for anything unrecognized.
	grep -F 'Pour ce terminal, lancez :' "$temp_root/empty-shell.out" >/dev/null || { printf 'an unknown SHELL must get the line to run\n' >&2; exit 1; }
	grep -F 'Ajouter la ligne suivante à' "$temp_root/empty-shell.out" >/dev/null && { printf 'an unknown SHELL must not be offered a profile edit\n' >&2; exit 1; }
	[ ! -e "$empty_home/.zshenv" ] || { printf 'empty SHELL must not be treated as zsh\n' >&2; exit 1; }
	[ ! -e "$empty_home/.profile" ] || { printf 'an unknown SHELL must not have a profile edited\n' >&2; exit 1; }

	# fish does not read ~/.profile, and a POSIX `export` means nothing to it:
	# offering to edit that file would report a change with no effect.
	fish_home="$temp_root/fish home"
	mkdir -p "$fish_home"
	python3 - "$fish_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/fish.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/usr/bin/fish", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -F 'Ajouter la ligne suivante à' "$temp_root/fish.out" >/dev/null && { printf 'fish must not be offered a POSIX profile edit\n' >&2; exit 1; }
	[ ! -e "$fish_home/.profile" ] || { printf 'fish must not have ~/.profile edited\n' >&2; exit 1; }

	# A HOME containing shell metacharacters must not break the printed
	# recovery commands: they reference "$HOME" literally rather than inlining
	# the expanded path.
	meta_home="$temp_root/met a\${x} home"
	mkdir -p "$meta_home"
	: > "$meta_home/.zshenv"
	python3 - "$meta_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/meta.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"),
    "FAKE_SETUP_EXIT": "1", "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"Reprenez" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	if grep -qF "$meta_home" "$temp_root/meta.out"; then
		printf 'the expanded HOME leaked into a printed recovery command\n' >&2; exit 1
	fi

	# An unwritable profile must not fail the install before the wizard runs:
	# the PATH edit is a convenience, the binary is already in place.
	# A directory in place of the profile file fails the append for any uid
	# (a chmod 0444 file does not stop root).
	ro_home="$temp_root/readonly home"
	mkdir -p "$ro_home/.zshenv"
	python3 - "$ro_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/readonly.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
setup_log = os.path.join(home, "setup-called")
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": setup_log,
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 8
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    # This block asserts that setup RAN, so the banner is not a completion
    # signal: it is printed before setup is exec'd. The marker the fake setup
    # writes is the condition the assertions depend on; the deadline above
    # stays as the backstop.
    if os.path.exists(setup_log): break
# The assertions grep the captured output, so the marker alone is not enough:
# bytes printed before it (the profile warning, the banner) can still be
# sitting unread in the pty buffer. Drain until the pty goes quiet.
quiet_deadline = time.time() + 2
while time.time() < quiet_deadline:
    r, _, _ = select.select([fd], [], [], 0.2)
    if not r: break
    try: chunk = os.read(fd, 4096)
    except OSError: break
    if not chunk: break
    out += chunk
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	# The banner is still worth asserting: it is what the user sees. It just
	# cannot double as a completion signal, since it precedes setup.
	grep -F 'Lancement de la configuration' "$temp_root/readonly.out" >/dev/null || { printf 'setup launch line missing\n' >&2; exit 1; }
	grep -F "Le profil ~/.zshenv n'a pas pu être modifié." "$temp_root/readonly.out" >/dev/null || { printf 'an unwritable profile must be reported, not fatal\n' >&2; exit 1; }
	[ -e "$ro_home/setup-called" ] || { printf 'an unwritable profile must not prevent the wizard from running\n' >&2; exit 1; }

	# A `codebox.previous` directory would swallow the backup silently.
	prev_home="$temp_root/prev home"
	mkdir -p "$prev_home/.local/bin/codebox.previous"
	env FAKE_OS=Linux FAKE_ARCH=x86_64 FAKE_ASSET=$asset FAKE_BINARY="$fake_binary" \
		FAKE_SUMS="$good_sums" FAKE_URL_LOG="$temp_root/prev-urls" \
		FAKE_RELEASE_URL="$release_url" FAKE_SETUP_LOG="$prev_home/setup-called" \
		FAKE_TARGET="$prev_home/.local/bin/codebox" REAL_MV="$real_mv" \
		HOME="$prev_home" PATH="$fake_bin:$PATH" \
		"$installer" > "$temp_root/prev.out" 2>&1 || true
	grep -F "n'est pas un fichier ordinaire" "$temp_root/prev.out" >/dev/null || { printf 'a non-regular backup path must be refused\n' >&2; exit 1; }
	[ ! -e "$prev_home/.local/bin/codebox.previous/codebox.previous" ] || { printf 'the backup was moved inside a directory\n' >&2; exit 1; }

	# Ctrl-C during setup: the signal must reach the wizard AND the wrapper must
	# still print the recovery guidance. Guarding the wrapper by IGNORING SIGINT
	# would look like it works here but would silently make the wizard
	# uninterruptible, because an ignored signal disposition is inherited
	# across exec.
	sig_home="$temp_root/sig home"
	mkdir -p "$sig_home"
	cat > "$temp_root/sig-setup" <<'EOS'
#!/bin/sh
[ "${1:-}" = setup ] || exit 0
printf called > "$FAKE_SETUP_LOG"
printf 'assistant demarre\n'
sleep 20
EOS
	chmod +x "$temp_root/sig-setup"
	sha256sum "$temp_root/sig-setup" | awk '{print $1"  codebox-linux-x64"}' > "$temp_root/sig-sums"
	fake_state="$temp_root/sig-bin"
	rm -rf "$fake_state"; mkdir -p "$fake_state"
	cat > "$fake_state/uname" <<'EOS'
#!/bin/sh
case "$1" in -s) echo Linux;; -m) echo x86_64;; *) exit 2;; esac
EOS
	cat > "$fake_state/curl" <<'EOS'
#!/bin/sh
out=''; url=''
while [ $# -gt 0 ]; do case "$1" in -o|--output) out=$2; shift 2;; -w|--write-out) shift 2;; -*) shift;; *) url=$1; shift;; esac; done
case "$url" in
	*/releases/latest) printf '%s' "$FAKE_RELEASE_URL";;
	*/SHA256SUMS) cp "$FAKE_SUMS" "$out";;
	*/"$FAKE_ASSET") cp "$FAKE_BINARY" "$out";;
	*) exit 22;;
esac
EOS
	chmod +x "$fake_state/uname" "$fake_state/curl"
	python3 - "$temp_root" "$installer" "$sig_home" "$fake_state" "$real_mv" <<'PY' > "$temp_root/sig.out" 2>&1
import os, pty, sys, time, select, signal
root, installer, home, fake_state, real_mv = sys.argv[1:6]
env = {
    "HOME": home, "PATH": f"{fake_state}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": "codebox-linux-x64",
    "FAKE_BINARY": os.path.join(root, "sig-setup"), "FAKE_SUMS": os.path.join(root, "sig-sums"),
    "FAKE_URL_LOG": os.path.join(root, "sig-urls"),
    "FAKE_RELEASE_URL": "https://github.com/etalab-ia/codebox/releases/tag/codebox-v9.8.7",
    "FAKE_SETUP_LOG": os.path.join(root, "sig-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
time.sleep(1.5)
os.write(fd, b"\n")          # accept the PATH prompt
time.sleep(2.0)              # let the wizard start
os.killpg(os.getpgid(pid), signal.SIGINT)   # what a terminal Ctrl-C sends
out = b""
deadline = time.time() + 10
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -F 'assistant demarre' "$temp_root/sig.out" >/dev/null || { printf 'the wizard never started\n' >&2; exit 1; }
	grep -F 'La configuration a été interrompue.' "$temp_root/sig.out" >/dev/null || { printf 'an interrupted setup must reach the recovery block\n' >&2; exit 1; }
	grep -F 'Reprenez-la quand vous voulez : "$HOME/.local/bin/codebox" setup' "$temp_root/sig.out" >/dev/null || { printf 'an interrupted setup must print the resume command\n' >&2; exit 1; }

	# Linux bash with an existing ~/.bashrc: a new terminal there is a
	# non-login shell, which reads ~/.bashrc and neither ~/.bash_profile nor
	# ~/.profile. Targeting the login files would leave codebox unavailable
	# until the user logged out and back in.
	rc_home="$temp_root/rc home"
	mkdir -p "$rc_home"
	printf 'export EDITOR=vi\n' > "$rc_home/.bashrc"
	python3 - "$rc_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/bashrc.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/bash", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -F 'Ajouter la ligne suivante à ~/.bashrc ?' "$temp_root/bashrc.out" >/dev/null || { printf 'linux bash must target ~/.bashrc\n' >&2; exit 1; }
	[ ! -e "$rc_home/.bash_profile" ] || { printf 'installer created a shadowing ~/.bash_profile on linux\n' >&2; exit 1; }
	grep -F 'export PATH="$HOME/.local/bin:$PATH"' "$rc_home/.bashrc" >/dev/null || { printf 'PATH line not written to ~/.bashrc\n' >&2; exit 1; }
	grep -F 'export EDITOR=vi' "$rc_home/.bashrc" >/dev/null || { printf 'existing ~/.bashrc content was destroyed\n' >&2; exit 1; }

	# macOS bash with no ~/.bash_profile: a login shell falls back to
	# ~/.profile, so that existing file is the one to extend. Creating a
	# ~/.bash_profile would shadow it and hide whatever it initialises.
	mac_home="$temp_root/mac home"
	mkdir -p "$mac_home"
	printf 'export EDITOR=vi\n' > "$mac_home/.profile"
	# The darwin leg downloads the darwin asset, so the fake curl must be
	# offered that name rather than the linux one.
	python3 - "$mac_home" "$installer" "$fake_bin" 'codebox-darwin-arm64' "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/mac-bash.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/bash", "TERM": "xterm",
    "FAKE_OS": "Darwin", "FAKE_ARCH": "arm64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -F 'Ajouter la ligne suivante à ~/.profile ?' "$temp_root/mac-bash.out" >/dev/null || { printf 'macos bash must extend an existing ~/.profile\n' >&2; exit 1; }
	[ ! -e "$mac_home/.bash_profile" ] || { printf 'installer shadowed ~/.profile with a new ~/.bash_profile\n' >&2; exit 1; }
	grep -F 'export PATH="$HOME/.local/bin:$PATH"' "$mac_home/.profile" >/dev/null || { printf 'PATH line not written to ~/.profile on macos\n' >&2; exit 1; }

	# A read that fails at the PATH prompt (Ctrl-D) is a refusal, never an
	# implicit "yes": closing stdin without answering must not edit a profile.
	ctrl_home="$temp_root/ctrl-d home"
	mkdir -p "$ctrl_home"
	printf 'export EDITOR=vi\n' > "$ctrl_home/.profile"
	python3 - "$ctrl_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/ctrl-d.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
# Ctrl-D on an empty line: the pending read returns EOF, not an empty answer.
os.write(fd, b"\x04")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	[ ! -e "$ctrl_home/.zshenv" ] || { printf 'a failed prompt read must not edit the profile\n' >&2; exit 1; }
	grep -F "Réponse non lue" "$temp_root/ctrl-d.out" >/dev/null || { printf 'a failed prompt read must be reported as a refusal\n' >&2; exit 1; }

	# Re-running the installer from the same terminal (where PATH is still
	# unchanged) must not append the stanza a second time: every new shell
	# would otherwise prepend ~/.local/bin once per install.
	dup_home="$temp_root/dup home"
	mkdir -p "$dup_home"
	printf '\n# codebox\nexport PATH="$HOME/.local/bin:$PATH"\n' > "$dup_home/.zshenv"
	python3 - "$dup_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/dup.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	count=$(grep -c 'local/bin' "$dup_home/.zshenv" || true)
	[ "$count" -eq 1 ] || { printf 'installer appended the PATH line again (count=%s)\n' "$count" >&2; exit 1; }
	grep -F 'La ligne est déjà présente dans ~/.zshenv.' "$temp_root/dup.out" >/dev/null || { printf 'installer did not report the already-present line\n' >&2; exit 1; }

	# A profile that merely MENTIONS codebox (an alias, a comment) must not be
	# mistaken for one that already exports the PATH: the exact line decides.
	mention_home="$temp_root/mention home"
	mkdir -p "$mention_home"
	printf "alias jc='codebox'\n# codebox notes\n" > "$mention_home/.zshenv"
	python3 - "$mention_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/mention.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	grep -qxF 'export PATH="$HOME/.local/bin:$PATH"' "$mention_home/.zshenv" || { printf 'a mere mention of codebox must not suppress the PATH line\n' >&2; exit 1; }
	grep -F 'La ligne a été ajoutée à ~/.zshenv.' "$temp_root/mention.out" >/dev/null || { printf 'installer did not report adding the line\n' >&2; exit 1; }

	# Conversely, the exact export present WITHOUT the marker must not be
	# duplicated either.
	exact_home="$temp_root/exact home"
	mkdir -p "$exact_home"
	printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$exact_home/.zshenv"
	python3 - "$exact_home" "$installer" "$fake_bin" "$asset" "$good_sums" "$release_url" "$fake_binary" "$real_mv" <<'PY' > "$temp_root/exact.out" 2>&1
import os, pty, sys, time, select
home, installer, fake_bin, asset, sums, relurl, binary, real_mv = sys.argv[1:9]
env = {
    "HOME": home, "PATH": f"{fake_bin}:/usr/bin:/bin", "SHELL": "/bin/zsh", "TERM": "xterm",
    "FAKE_OS": "Linux", "FAKE_ARCH": "x86_64", "FAKE_ASSET": asset, "FAKE_BINARY": binary,
    "FAKE_SUMS": sums, "FAKE_URL_LOG": os.path.join(home, "urls"),
    "FAKE_RELEASE_URL": relurl, "FAKE_SETUP_LOG": os.path.join(home, "setup-called"),
    "FAKE_TARGET": os.path.join(home, ".local/bin/codebox"), "REAL_MV": real_mv,
}
pid, fd = pty.fork()
if pid == 0:
    os.execve("/bin/sh", ["sh", installer], env)
out = b""
time.sleep(1.2)
os.write(fd, b"\n")
deadline = time.time() + 6
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.4)
    if r:
        try: chunk = os.read(fd, 4096)
        except OSError: break
        if not chunk: break
        out += chunk
    if b"configuration" in out: break
sys.stdout.write(out.decode("utf-8", "replace"))
PY
	exact_count=$(grep -cxF 'export PATH="$HOME/.local/bin:$PATH"' "$exact_home/.zshenv" || true)
	[ "$exact_count" -eq 1 ] || { printf 'an exact export line must not be duplicated (count=%s)\n' "$exact_count" >&2; exit 1; }
# The resume command must name the installed binary by absolute path: the
# invoking shell may not have ~/.local/bin on PATH (the very case handled
# above), so a bare `codebox setup` would be a command not found.
# Quoted, so the line survives a HOME containing spaces (this test's $home has
# one); a bare absolute path would split at the space when pasted.
# A literal "$HOME/..." is printed instead of the expanded path, so a HOME
# containing shell metacharacters cannot break the copied command.
# The command must name the BINARY, not the containing directory: pasting a
# directory path fails with "Is a directory".
grep -F 'Reprenez-la quand vous voulez : "$HOME/.local/bin/codebox" setup' "$temp_root/setup-failure.out" >/dev/null || { printf 'setup failure omitted a resolvable resume command\n' >&2; exit 1; }
grep -F "codebox est installé, mais la configuration n'est pas terminée." "$temp_root/setup-failure.out" >/dev/null || { printf 'setup failure message is not in French\n' >&2; exit 1; }

unsupported_home="$temp_root/unsupported"
mkdir -p "$unsupported_home"
: > "$temp_root/unsupported-urls"
if env FAKE_OS=Darwin FAKE_ARCH=x86_64 FAKE_ASSET=$asset FAKE_BINARY="$fake_binary" \
	FAKE_SUMS="$good_sums" FAKE_URL_LOG="$temp_root/unsupported-urls" \
	FAKE_RELEASE_URL="$release_url" FAKE_SETUP_LOG="$temp_root/setup-called" \
	FAKE_TARGET="$unsupported_home/codebox" REAL_MV="$real_mv" \
	HOME="$unsupported_home" PATH="$fake_bin:$PATH" \
	"$installer" > "$temp_root/unsupported.out" 2>&1; then
	printf 'unsupported macOS Intel target unexpectedly succeeded\n' >&2
	exit 1
fi
[ ! -s "$temp_root/unsupported-urls" ] || { printf 'unsupported target performed network requests\n' >&2; exit 1; }

blocked_home="$temp_root/home with blocked bin"
mkdir -p "$blocked_home/.local"
printf 'not a directory\n' > "$blocked_home/.local/bin"
: > "$temp_root/blocked-urls"
if env FAKE_OS=Linux FAKE_ARCH=x86_64 FAKE_ASSET=$asset FAKE_BINARY="$fake_binary" \
	FAKE_SUMS="$good_sums" FAKE_URL_LOG="$temp_root/blocked-urls" \
	FAKE_RELEASE_URL="$release_url" FAKE_SETUP_LOG="$temp_root/setup-called" \
	FAKE_TARGET="$blocked_home/.local/bin/codebox" REAL_MV="$real_mv" \
	HOME="$blocked_home" PATH="$fake_bin:$PATH" \
	"$installer" > "$temp_root/blocked.out" 2>&1; then
	printf 'an unusable installation directory unexpectedly succeeded\n' >&2
	exit 1
fi
[ ! -s "$temp_root/blocked-urls" ] || { printf 'permission failure performed network requests\n' >&2; exit 1; }

printf 'POSIX installer checks passed\n'
