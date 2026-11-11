#!/bin/sh
set -eu

REPOSITORY='etalab-ia/codebox'
RELEASE_PAGE="https://github.com/$REPOSITORY/releases/latest"
RELEASE_PREFIX="https://github.com/$REPOSITORY/releases/tag/"
DOWNLOAD_PREFIX="https://github.com/$REPOSITORY/releases/download"
SUPPORTED_HOSTS='macOS Apple Silicon, Linux arm64 et Linux x64 (Windows : utilisez install.ps1)'

die() {
	printf 'codebox : %s\n' "$1" >&2
	exit 1
}

system=$(uname -s) || die 'système d'"'"'exploitation indétectable'
machine=$(uname -m) || die 'architecture machine indétectable'
case "$system:$machine" in
	Darwin:arm64|Darwin:aarch64)
		asset='codebox-darwin-arm64'
		platform='macOS, Apple Silicon'
		;;
	Darwin:*)
		die "codebox ne fonctionne pas sur ce poste (macOS Intel).
Postes pris en charge : $SUPPORTED_HOSTS."
		;;
	Linux:x86_64|Linux:amd64)
		asset='codebox-linux-x64'
		platform='Linux x64'
		;;
	Linux:arm64|Linux:aarch64)
		asset='codebox-linux-arm64'
		platform='Linux arm64'
		;;
	Linux:*)
		die "codebox ne fonctionne pas sur ce poste ($system/$machine).
Postes pris en charge : $SUPPORTED_HOSTS."
		;;
	*)
		die "codebox ne fonctionne pas sur ce poste ($system).
Postes pris en charge : $SUPPORTED_HOSTS."
		;;
esac

if [ -z "${HOME:-}" ]; then
	die 'la variable HOME n'"'"'est pas définie ; impossible de choisir un répertoire d'"'"'installation.'
fi
install_dir="$HOME/.local/bin"
# The same path relative to $HOME, for the recovery commands that are printed
# for the user to copy: a literal "$HOME/..." survives a HOME containing shell
# metacharacters, which an interpolated absolute path would not.
install_dir_rel="${install_dir#"$HOME"/}"
mkdir -p "$install_dir" || die "création impossible de $install_dir ; choisissez un répertoire accessible en écriture."
tmp_dir=$(mktemp -d "$install_dir/.codebox-install.XXXXXX") || die "création impossible d'un répertoire temporaire sous $install_dir"
trap 'rm -rf "$tmp_dir"' 0
# Une interruption (Ctrl-C) doit passer par le même bloc de reprise qu'un échec
# de l'assistant : sortir directement sur le signal priverait l'utilisateur de
# la commande de reprise, alors que c'est le cas le plus fréquent.
interrupted=0
trap 'interrupted=1' HUP INT TERM

target="$install_dir/codebox"
backup="$install_dir/codebox.previous"
if [ -L "$target" ]; then
	die "$target est un lien symbolique ; remplacement refusé."
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
	die "$target n'est pas un fichier ordinaire ; remplacement refusé."
fi
# Le même garde-fou pour le retour arrière : un `codebox.previous` qui serait
# un répertoire ferait déplacer la sauvegarde À L'INTÉRIEUR de lui, et le
# binaire précédent deviendrait introuvable sans aucun signalement.
if [ -e "$backup" ] && [ ! -f "$backup" ]; then
	die "$backup n'est pas un fichier ordinaire ; retour arrière refusé, déplacez-le d'abord."
fi

command -v curl >/dev/null 2>&1 || die 'curl est nécessaire pour télécharger les fichiers de la release.'
release_url=$(curl --fail --silent --show-error --location --output /dev/null --write-out '%{url_effective}' "$RELEASE_PAGE") || die 'résolution de la dernière release stable GitHub impossible'
case "$release_url" in
	"$RELEASE_PREFIX"*) tag=${release_url#"$RELEASE_PREFIX"} ;;
	*) die "URL de release inattendue : $release_url" ;;
esac
printf '%s\n' "$tag" | grep -Eq '^codebox-v?[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$' || die "étiquette de release inattendue : $tag"
version=${tag#codebox-}
version=${version#v}

printf 'Installation de codebox\n\n'

download() {
	url=$1
	destination=$2
	curl --fail --silent --show-error --location --output "$destination" "$url" || die "téléchargement impossible : $url"
	[ -s "$destination" ] || die "téléchargement vide : $url"
}

download "$DOWNLOAD_PREFIX/$tag/$asset" "$tmp_dir/$asset"
download "$DOWNLOAD_PREFIX/$tag/SHA256SUMS" "$tmp_dir/SHA256SUMS"
printf '  ✓ Version %s téléchargée (%s)\n' "$version" "$platform"

expected=$(awk -v asset="$asset" '
	{
		name = $2
		sub(/^\*/, "", name)
		if (name == asset) {
			count++
			hash = $1
		}
	}
	END {
		if (count != 1) exit 1
		print hash
	}
' "$tmp_dir/SHA256SUMS") || die "SHA256SUMS ne contient pas exactement une entrée pour $asset"
case "$expected" in
	*[!0123456789abcdefABCDEF]*|'') die "entrée de somme de contrôle invalide pour $asset" ;;
esac
[ "${#expected}" -eq 64 ] || die "longueur de somme de contrôle invalide pour $asset"

if command -v sha256sum >/dev/null 2>&1; then
	hash_output=$(sha256sum "$tmp_dir/$asset") || die 'échec de sha256sum'
elif command -v shasum >/dev/null 2>&1; then
	hash_output=$(shasum -a 256 "$tmp_dir/$asset") || die 'échec de shasum'
else
	die 'aucun outil SHA-256 trouvé (sha256sum ou shasum requis)'
fi
actual=$(printf '%s\n' "$hash_output" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
expected=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
if [ "$actual" != "$expected" ]; then
	printf 'codebox : somme de contrôle non concordante pour %s ; installation existante inchangée.\n' "$asset" >&2
	exit 1
fi
printf '  ✓ Intégrité vérifiée\n'

chmod 0755 "$tmp_dir/$asset" || die 'impossible de rendre le binaire vérifié exécutable'
if [ -e "$target" ]; then
	cp -p "$target" "$tmp_dir/codebox.previous" || die 'impossible de conserver le binaire actuel pour un retour arrière'
	mv -f "$tmp_dir/codebox.previous" "$backup" || die 'impossible d'"'"'enregistrer le binaire de retour arrière ; installation inchangée'
fi
mv -f "$tmp_dir/$asset" "$target" || die "impossible de remplacer $target ; le binaire précédent reste disponible"
printf '  ✓ Installé dans ~/.local/bin/codebox\n\n'

# PATH avant le setup : sinon le message de fin de l'assistant renvoie vers une
# commande introuvable dans un nouveau terminal.
case ":${PATH:-}:" in
	*":$install_dir:"*) ;;
	*)
		shell_name=$(basename "${SHELL:-}" 2>/dev/null || printf 'sh')
		# Printed literally to the user; the single quotes are intentional.
		# shellcheck disable=SC2016
		path_line='export PATH="$HOME/.local/bin:$PATH"'
		# ~/.zshenv pour zsh (fichier lu par tous les shells zsh, comme le
		# faisait Albert Code) ; repli POSIX pour les autres shells, y compris
		# un SHELL vide (inconnu), qu'il ne faut pas traiter comme zsh.
		# The tilde is displayed as-is and stripped by the ${path_file#\~/} below.
		# shellcheck disable=SC2088
		case "$shell_name" in
			zsh) path_file='~/.zshenv' ;;
			bash)
				# Bash ne lit pas le même fichier selon le type de shell, et le
				# type dépend de la plateforme : un Terminal macOS ouvre un
				# shell de connexion (~/.bash_profile, avec repli sur
				# ~/.profile), un terminal Linux ouvre un shell interactif non
				# connecté (~/.bashrc). On ne vise que le fichier que les
				# nouveaux terminaux de cette plateforme lisent vraiment.
				if [ "$system" = 'Darwin' ]; then
					# macOS : fichier de connexion, par ordre de préférence le
					# premier déjà présent. Un ~/.bash_profile créé de zéro
					# masquerait un ~/.profile existant, que bash de connexion
					# ne lirait plus — d'où .profile avant la création.
					if [ -f "$HOME/.bash_profile" ]; then
						path_file='~/.bash_profile'
					elif [ -f "$HOME/.profile" ]; then
						path_file='~/.profile'
					else
						path_file='~/.bash_profile'
					fi
				else
					# Linux : un nouveau terminal est un shell non connecté, qui
					# ne lit QUE ~/.bashrc. Les fichiers de connexion n'y
					# changeraient rien, et ~/.bashrc ne masque rien.
					path_file='~/.bashrc'
				fi
				;;
			*)
				# sh/dash/ksh lisent ~/.profile ; fish et nushell ne le lisent
				# pas, et y écrire un `export` POSIX ne changerait rien pour
				# eux. Mieux vaut ne rien proposer que d'annoncer un ajout
				# sans effet.
				case "$shell_name" in
					sh|dash|ksh|ash) path_file='~/.profile' ;;
					fish|nu|nushell) path_file='' ;;
					*) path_file='' ;;
				esac
				;;
		esac
		if [ -z "$path_file" ]; then
			# Shell non POSIX (fish, nushell) ou inconnu : aucun fichier de
			# démarrage commun à modifier, et un `export` POSIX n'y aurait pas
			# d'effet. On laisse le message non interactif ci-dessous donner la
			# ligne à exécuter, sans prétendre l'avoir installée.
			path_target=''
		else
			path_target="$HOME/${path_file#\~\/}"
		fi
		# On ne modifie le profil que si l'on peut poser la question : un
		# script non interactif ne doit jamais éditer un fichier de shell.
		if [ -z "$path_target" ]; then
			can_prompt=0
		elif [ -t 0 ]; then
			can_prompt=1
		elif ( : < /dev/tty ) 2>/dev/null; then
			can_prompt=1
		else
			can_prompt=0
		fi
		if [ "$can_prompt" -eq 1 ]; then
			printf 'Le dossier ~/.local/bin n'"'"'est pas dans votre PATH : la commande codebox\n'
			printf 'ne serait pas trouvée dans un nouveau terminal.\n'
			printf 'Ajouter la ligne suivante à %s ? (O/n)\n' "$path_file"
			printf '  %s\n\n' "$path_line"
			# Une lecture qui échoue (Ctrl-D, /dev/tty devenu illisible) ne
			# vaut pas acceptation : c'est un refus, jamais un « oui » implicite.
			answer=''
			read_ok=0
			if [ -t 0 ]; then
				read -r answer && read_ok=1
			else
				read -r answer < /dev/tty && read_ok=1
			fi
			if [ "$read_ok" -eq 0 ]; then
				printf 'Réponse non lue : le profil %s n'"'"'a pas été modifié.\n\n' "$path_file"
				answer='n'
			fi
			case "$answer" in
				''|o|O|y|Y)
					# Comparer la ligne EXACTE, pas le mot « codebox » : un alias
					# ou un commentaire qui le mentionne donnerait un faux positif
					# et la ligne ne serait jamais ajoutée.
					if [ -f "$path_target" ] && grep -qxF "$path_line" "$path_target" 2>/dev/null; then
						# Déjà ajoutée par une installation précédente : réinstaller
						# ne doit pas empiler la même ligne (chaque nouveau shell
						# préfixerait alors le PATH une fois de plus).
						printf 'La ligne est déjà présente dans %s.\n\n' "$path_file"
					else
						# Une modification de profil ratée (fichier en lecture
						# seule, montage en lecture seule, permission) ne doit pas
						# faire échouer l'installation : le binaire est déjà en
						# place et l'assistant doit encore démarrer. On retombe sur
						# le message « ligne non ajoutée », qui donne la commande
						# à exécuter dans la session courante.
						if {
							printf '\n# codebox\n'
							printf '%s\n' "$path_line"
						} >> "$path_target" 2>/dev/null; then
							printf 'La ligne a été ajoutée à %s.\n\n' "$path_file"
						else
							printf 'Le profil %s n'"'"'a pas pu être modifié.\n\n' "$path_file"
							printf 'Ligne non ajoutée. Pour ce terminal, lancez :\n  %s\n\n' "$path_line"
						fi
					fi
					;;
				*)
					printf 'Ligne non ajoutée. Pour ce terminal, lancez :\n  %s\n\n' "$path_line"
					;;
			esac
		else
			printf 'Le dossier ~/.local/bin n'"'"'est pas dans votre PATH : la commande codebox\n'
			printf 'ne serait pas trouvée dans un nouveau terminal.\n'
			printf 'Pour ce terminal, lancez :\n  %s\n\n' "$path_line"
		fi
		;;
esac

printf 'Lancement de la configuration…\n\n'
setup_status=0
if [ ! -t 0 ] && ( : < /dev/tty ) 2>/dev/null; then
	# Lancé depuis un tube (curl | sh) : l'entrée standard est le tube épuisé,
	# on réattache le terminal de contrôle pour garder l'assistant interactif.
	"$target" setup < /dev/tty || setup_status=$?
else
	"$target" setup || setup_status=$?
fi
if [ "$setup_status" -ne 0 ]; then
	printf 'codebox est installé, mais la configuration n'"'"'est pas terminée.\n' >&2
	if [ "$interrupted" -eq 1 ]; then
		printf 'La configuration a été interrompue.\n' >&2
	fi
	# Le terminal courant n'a pas forcément ~/.local/bin dans son PATH (c'est
	# le cas que le bloc PATH vient de traiter) : donner le chemin absolu, sinon
	# la commande de reprise ne serait pas trouvée dans cette session.
	# Le chemin est écrit littéralement en "$HOME/..." plutôt qu'interpolé : un
	# HOME contenant des caractères de shell ($, guillemet, espace) casserait la
	# commande collée. "$HOME" entre guillemets est sûr et se développe à
	# l'exécution.
	# shellcheck disable=SC2016  # literal "$HOME" is what the user should copy
	printf 'Reprenez-la quand vous voulez : "$HOME/%s/codebox" setup\n' "$install_dir_rel" >&2
	if [ -f "$backup" ]; then
		# shellcheck disable=SC2016
		printf 'Pour revenir à la version précédente : mv "$HOME/%s/codebox.previous" "$HOME/%s/codebox"\n' "$install_dir_rel" "$install_dir_rel" >&2
	fi
	exit "$setup_status"
fi
