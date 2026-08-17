#!/bin/sh
set -eu

usage() {
	echo "usage: $0 [--days N] [--execute]" >&2
}

days=30
execute=0
while [ "$#" -gt 0 ]; do
	case "$1" in
	--days)
		[ "$#" -ge 2 ] || { usage; exit 2; }
		days=$2
		shift 2
		;;
	--execute) execute=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage; exit 2 ;;
	esac
done
case "$days" in ''|*[!0-9]*) echo "days must be a positive integer" >&2; exit 2 ;; esac
[ "$days" -gt 0 ] || { echo "days must be a positive integer" >&2; exit 2; }

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
root=$repo/backups
[ -d "$root" ] || { echo "no backup directory"; exit 0; }
mtime=$((days - 1))

find "$root" -type f \( -name '*.tar.gz' -o -name '*.tar.gz.sha256' \) -mtime "+$mtime" | while IFS= read -r file; do
	if [ "$execute" -eq 1 ]; then
		echo "DELETE $file"
		rm -f -- "$file"
	else
		echo "DRY-RUN $file"
	fi
done

if [ "$execute" -eq 1 ]; then
	echo "expired local backup files deleted"
else
	echo "dry run complete; rerun with --execute after review"
fi
