#!/bin/sh
set -eu

usage() {
	echo "usage: $0 [--days N] [--execute]" >&2
}

inside=0
execute=0
days=30
while [ "$#" -gt 0 ]; do
	case "$1" in
	--inside) inside=1; shift ;;
	--execute) execute=1; shift ;;
	--days)
		[ "$#" -ge 2 ] || { usage; exit 2; }
		days=$2
		shift 2
		;;
	-h | --help) usage; exit 0 ;;
	*) usage; exit 2 ;;
	esac
done
case "$days" in ''|*[!0-9]*) echo "days must be a positive integer" >&2; exit 2 ;; esac
[ "$days" -gt 0 ] || { echo "days must be a positive integer" >&2; exit 2; }

if [ "$inside" -eq 0 ]; then
	repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
	if [ "$execute" -eq 1 ]; then
		if ! states=$(docker compose -f "$repo/compose.yaml" ps -a --format '{{.Service}} {{.State}}' adapter demarkus); then
			echo "cannot verify adapter and demarkus state" >&2
			exit 1
		fi
		case "$states" in
		*" running"* | *" restarting"* | *" paused"*)
			echo "--execute requires stopped adapter and demarkus services" >&2
			exit 1
			;;
		esac
	fi
	if [ "$execute" -eq 1 ]; then
		exec docker compose -f "$repo/compose.yaml" run --rm --no-deps --user 65532:65532 \
			-v "$repo/scripts/purge-expired.sh:/purge-expired.sh:ro" volume-init \
			sh /purge-expired.sh --inside --days "$days" --execute
	fi
	exec docker compose -f "$repo/compose.yaml" run --rm --no-deps --user 65532:65532 \
		-v "$repo/scripts/purge-expired.sh:/purge-expired.sh:ro" volume-init \
		sh /purge-expired.sh --inside --days "$days"
fi

root=/demarkus-data/u
if [ ! -d "$root" ]; then
	echo "no user data directory"
	exit 0
fi

cutoff=$(( $(date +%s) - days * 86400 ))
list=/tmp/purge-candidates
find "$root" -mindepth 4 -maxdepth 4 -type l -name '*.md' > "$list"
count=0

valid_segment() {
	case "$1" in ''|*[!a-z2-7]*) return 1 ;; *) return 0 ;; esac
}

while IFS= read -r doc; do
	rel=${doc#"$root"/}
	user=${rel%%/*}
	rest=${rel#*/}
	[ "$rest" != "$rel" ] || continue
	marker=${rest%%/*}
	rest=${rest#*/}
	session=${rest%%/*}
	base=${rest#*/}
	[ "$base" != "$rest" ] || continue
	case "$base" in */*) continue ;; esac
	stem=${base%.md}
	[ "$base" != "$stem" ] || continue
	valid_segment "$user" || continue
	[ "$marker" = sessions ] || continue
	valid_segment "$session" || continue
	valid_segment "$stem" || continue

	target=$(readlink "$doc") || continue
	version=${target##*/}
	digits=${version#v}
	[ "$digits" != "$version" ] || continue
	case "$digits" in ''|*[!0-9]*) continue ;; esac
	[ "$target" = "versions/$base/$version" ] || continue
	version_dir=$(dirname "$doc")/versions/$base
	target_path=$(dirname "$doc")/$target
	[ -f "$target_path" ] || continue
	modified=$(stat -c %Y "$target_path") || continue
	[ "$modified" -lt "$cutoff" ] || continue

	count=$((count + 1))
	if [ "$execute" -eq 1 ]; then
		echo "DELETE $doc"
		rm -f -- "$doc"
		rm -rf -- "$version_dir"
	else
		echo "DRY-RUN $doc"
	fi
done < "$list"

if [ "$execute" -eq 1 ]; then
	echo "deleted $count expired user documents"
else
	echo "$count expired user documents; rerun with --execute after backup and service stop"
fi
