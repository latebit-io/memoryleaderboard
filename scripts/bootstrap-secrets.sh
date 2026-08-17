#!/bin/bash
set -euo pipefail

usage() {
	echo "usage: $0 [--llm-key-file PATH]" >&2
}

llm_key_file=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--llm-key-file)
		[ "$#" -ge 2 ] || { usage; exit 2; }
		llm_key_file=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
	esac
done

repo=$(cd "$(dirname "$0")/.." && pwd)
secret_dir="$repo/secrets"
targets=(adapter-api-key demarkus-token demarkus-tokens.toml llm-api-key)

command -v openssl >/dev/null 2>&1 || {
	echo "openssl is required" >&2
	exit 1
}
mkdir -p "$secret_dir"
chmod 700 "$secret_dir"
for target in "${targets[@]}"; do
	if [ -e "$secret_dir/$target" ]; then
		echo "refusing to overwrite $secret_dir/$target" >&2
		echo "remove all generated secret files while services are stopped to rotate them" >&2
		exit 1
	fi
done

if [ -n "$llm_key_file" ]; then
	[ -r "$llm_key_file" ] || { echo "LLM key file is unreadable" >&2; exit 1; }
	IFS= read -r llm_key < "$llm_key_file" || true
elif [ -t 0 ]; then
	IFS= read -r -s -p "LLM API key: " llm_key
	printf '\n'
else
	echo "use --llm-key-file when stdin is not a terminal" >&2
	exit 1
fi
[ -n "${llm_key:-}" ] || { echo "LLM API key cannot be empty" >&2; exit 1; }

umask 077
tmp=$(mktemp -d "$secret_dir/.bootstrap.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

adapter_key=$(openssl rand -hex 32)
demarkus_token=$(openssl rand -hex 32)
digest=$(printf '%s' "$demarkus_token" | openssl dgst -sha256 -r)
digest=${digest%% *}

printf '%s\n' "$adapter_key" > "$tmp/adapter-api-key"
printf '%s\n' "$demarkus_token" > "$tmp/demarkus-token"
printf '%s\n' "$llm_key" > "$tmp/llm-api-key"
cat > "$tmp/demarkus-tokens.toml" <<EOF
[tokens.adapter]
hash = "sha256-$digest"
paths = ["/u/**"]
operations = ["read", "publish"]
EOF
# Compose bind-mounts local secret files without applying uid/gid/mode.
# The 0700 parent protects host access; other-read lets UID 65532 read mounts.
chmod 0404 "$tmp"/*

for target in "${targets[@]}"; do
	mv "$tmp/$target" "$secret_dir/$target"
done

echo "created adapter, demarkus, and LLM secrets in $secret_dir"
