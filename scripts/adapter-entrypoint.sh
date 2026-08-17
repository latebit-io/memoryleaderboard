#!/bin/sh
set -eu

read_secret() {
	name=$1
	path=$2
	if [ ! -r "$path" ]; then
		echo "required secret is unreadable: $path" >&2
		exit 1
	fi
	value=$(cat "$path")
	if [ -z "$value" ]; then
		echo "required secret is empty: $path" >&2
		exit 1
	fi
	export "$name=$value"
}

read_secret ADAPTER_API_KEY /run/secrets/adapter_api_key
read_secret DEMARKUS_TOKEN /run/secrets/demarkus_token
read_secret ADAPTER_LLM_API_KEY /run/secrets/llm_api_key

exec /usr/local/bin/adapter
