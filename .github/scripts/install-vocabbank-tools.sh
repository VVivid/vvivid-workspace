#!/bin/bash
set -euo pipefail

# VocabBank check/release tooling; the checksum matches the shared pinned Helm version.
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]]
tools_dir=$(mktemp -d "${RUNNER_TEMP:-/tmp}/vocabbank-tools.XXXXXX")
curl --fail --silent --show-error --location \
  https://get.helm.sh/helm-v4.3.1-linux-amd64.tar.gz --output "$tools_dir/helm.tar.gz"
printf '%s  %s\n' 0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b \
  "$tools_dir/helm.tar.gz" | sha256sum --check
tar -xzf "$tools_dir/helm.tar.gz" -C "$tools_dir" linux-amd64/helm
mv "$tools_dir/linux-amd64/helm" "$tools_dir/helm"
printf '%s\n' "$tools_dir" >> "$GITHUB_PATH"
