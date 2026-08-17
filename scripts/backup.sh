#!/bin/sh
set -eu
umask 077

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
output=${1:-"$repo/backups/demarkus-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"}
case "$output" in
*.tar.gz) ;;
*) echo "backup path must end in .tar.gz" >&2; exit 2 ;;
esac

running=$(docker compose -f "$repo/compose.yaml" ps --status running -q adapter demarkus 2>/dev/null || true)
if [ -n "$running" ]; then
	echo "stop adapter and demarkus before taking a filesystem backup" >&2
	exit 1
fi

out_dir=$(dirname "$output")
name=$(basename "$output")
mkdir -p "$out_dir"
out_dir=$(CDPATH='' cd -- "$out_dir" && pwd)
[ ! -e "$out_dir/$name" ] || { echo "refusing to overwrite $out_dir/$name" >&2; exit 1; }

docker compose -f "$repo/compose.yaml" run --rm --no-deps --user 0:0 --cap-add DAC_OVERRIDE \
	-e "BACKUP_NAME=$name" -e "BACKUP_UID=$(id -u)" -e "BACKUP_GID=$(id -g)" \
	-v "$out_dir:/backup" volume-init sh -ec \
	'umask 077 && tar -C /demarkus-data -czf "/backup/$BACKUP_NAME" . && chown "$BACKUP_UID:$BACKUP_GID" "/backup/$BACKUP_NAME"'

if command -v sha256sum >/dev/null 2>&1; then
	(cd "$out_dir" && sha256sum "$name" > "$name.sha256")
else
	(cd "$out_dir" && shasum -a 256 "$name" > "$name.sha256")
fi
chmod 600 "$out_dir/$name" "$out_dir/$name.sha256"
echo "$out_dir/$name"
