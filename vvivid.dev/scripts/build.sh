#!/bin/sh
set -eu

project_dir=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$project_dir"

hugo_binary=${HUGO:-hugo}
required_version=$(cat .hugo-version)
actual_version=$("$hugo_binary" version)
case "$actual_version" in
    "hugo v${required_version}-"*"+extended"* | "hugo v${required_version}+extended"*) ;;
    *)
        printf 'Hugo extended %s is required (found: %s).\n' "$required_version" "$actual_version" >&2
        exit 1
        ;;
esac

destination=${HUGO_DESTINATION:-public}
set -- --minify --environment production --destination "$destination"
if [ -n "${HUGO_BASEURL:-}" ]; then
    set -- "$@" --baseURL "$HUGO_BASEURL"
fi
"$hugo_binary" "$@"

leaked_guide=$(find "$destination" -type f \( -iname 'agents.md' -o -iname 'agent.md' \) -print -quit)
if [ -n "$leaked_guide" ]; then
    printf 'Agent guide leaked into public output: %s\n' "$leaked_guide" >&2
    exit 1
fi
