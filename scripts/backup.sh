#!/bin/sh
set -eu
umask 077

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
output=${1:-"$repo/backups/demarkus-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"}
case "$output" in
*.tar.gz) ;;
*) echo "backup path must end in .tar.gz" >&2; exit 2 ;;
esac

compose() {
	docker compose -f "$repo/compose.yaml" "$@"
}

archive=""
checksum=""
tmp_archive=""
tmp_checksum=""
restart_services=""
published=0
owns_final=0
lock_dir=$repo/.backup.lock
if ! mkdir "$lock_dir" 2>/dev/null; then
	echo "another backup is active; remove stale $lock_dir only after verifying no backup is running" >&2
	exit 1
fi

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ -n "$tmp_archive" ]; then
		rm -f -- "$tmp_archive" "$tmp_checksum" || true
	fi
	if [ -n "$restart_services" ]; then
		# Word splitting is intentional: Compose prints one service per line.
		# shellcheck disable=SC2086
		if ! compose start $restart_services; then
			echo "failed to restart: $restart_services" >&2
			status=1
		fi
	fi
	if [ "$published" -eq 0 ] && [ "$owns_final" -eq 1 ]; then
		rm -f -- "$archive" "$checksum" || true
	fi
	rmdir "$lock_dir" 2>/dev/null || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

out_dir=$(dirname "$output")
name=$(basename "$output")
mkdir -p "$out_dir"
out_dir=$(CDPATH='' cd -- "$out_dir" && pwd)
archive=$out_dir/$name
checksum=$archive.sha256
[ ! -e "$archive" ] && [ ! -e "$checksum" ] || {
	echo "refusing to overwrite $archive or $checksum" >&2
	exit 1
}
owns_final=1

tmp_name=.$name.partial.$$
tmp_checksum_name=.$name.sha256.partial.$$
tmp_archive=$out_dir/$tmp_name
tmp_checksum=$out_dir/$tmp_checksum_name

if ! service_states=$(compose ps -a --format '{{.Service}} {{.State}}' adapter demarkus); then
	echo "cannot determine service state" >&2
	exit 1
fi
restart_services=$(printf '%s\n' "$service_states" | while read -r service state; do
	case "$state" in
	running|restarting|paused) printf '%s\n' "$service" ;;
	esac
done)
if [ -n "$service_states" ]; then
	# Word splitting is intentional: Compose prints one service per line.
	# shellcheck disable=SC2086
	compose stop adapter demarkus
fi

# Variables expand inside the container shell, not this script.
# shellcheck disable=SC2016
compose run --rm --no-deps --user 0:0 --cap-add DAC_OVERRIDE \
	-e "BACKUP_NAME=$tmp_name" -e "BACKUP_UID=$(id -u)" -e "BACKUP_GID=$(id -g)" \
	-v "$out_dir:/backup" volume-init sh -ec \
	'umask 077 && tar -C /demarkus-data -czf "/backup/$BACKUP_NAME" . && chown "$BACKUP_UID:$BACKUP_GID" "/backup/$BACKUP_NAME"'

if command -v sha256sum >/dev/null 2>&1; then
	line=$(sha256sum "$tmp_archive")
else
	line=$(shasum -a 256 "$tmp_archive")
fi
digest=${line%% *}
printf '%s  %s\n' "$digest" "$name" > "$tmp_checksum"
chmod 600 "$tmp_archive" "$tmp_checksum"
mv "$tmp_archive" "$archive"
mv "$tmp_checksum" "$checksum"
published=1

if [ -n "$restart_services" ]; then
	# Word splitting is intentional: Compose prints one service per line.
	# shellcheck disable=SC2086
	compose start $restart_services
	restart_services=""
fi

echo "$archive"
