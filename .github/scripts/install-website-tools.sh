#!/bin/bash
set -euo pipefail

# GitHub's Linux amd64 runner; keep the Hugo pin aligned with .hugo-version.
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]]
tools_dir=$(mktemp -d "${RUNNER_TEMP:-/tmp}/website-tools.XXXXXX")
curl --fail --silent --show-error --location \
  https://github.com/gohugoio/hugo/releases/download/v0.162.1/hugo_extended_0.162.1_linux-amd64.tar.gz \
  --output "$tools_dir/hugo.tar.gz"
printf '%s  %s\n' e34160095b6a6406af857fe212f50e4451f67ed1276b9bb0de13d08754980118 \
  "$tools_dir/hugo.tar.gz" | sha256sum --check
tar -xzf "$tools_dir/hugo.tar.gz" -C "$tools_dir" hugo
curl --fail --silent --show-error --location \
  https://get.helm.sh/helm-v4.3.1-linux-amd64.tar.gz --output "$tools_dir/helm.tar.gz"
printf '%s  %s\n' 0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b \
  "$tools_dir/helm.tar.gz" | sha256sum --check
tar -xzf "$tools_dir/helm.tar.gz" -C "$tools_dir" linux-amd64/helm
mv "$tools_dir/linux-amd64/helm" "$tools_dir/helm"
printf '%s\n' "$tools_dir" >> "$GITHUB_PATH"
