#!/usr/bin/env bash
set -euo pipefail

# Fetch the exact local WeChat runtime that is embedded in a work-cli release.
# Assets stay generated/ignored; the version file is the reviewed release pin.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${WECHAT_CLI_VERSION:-$(tr -d '[:space:]' < "$root/scripts/wechat-runtime-version")}"
repo="${WECHAT_CLI_REPOSITORY:-rayson-x/wechat-cli}"
assets_dir="$root/internal/wechatruntime/assets"
base="https://github.com/$repo/releases/download/$version"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "invalid WECHAT_CLI_VERSION: $version" >&2
  exit 2
fi

rm -rf "$assets_dir"
mkdir -p "$assets_dir"
curl --fail --location --retry 3 --output "$assets_dir/SHA256SUMS.txt" "$base/SHA256SUMS.txt"

assets=(
  wechat-cli-windows-x64.exe
  wechat-cli-macos-x64
  wechat-cli-macos-arm64
)
for asset in "${assets[@]}"; do
  curl --fail --location --retry 3 --output "$assets_dir/$asset" "$base/$asset"
  expected="$(awk -v file="$asset" '$2 == file || $2 == ("*" file) { print $1; exit }' "$assets_dir/SHA256SUMS.txt")"
  actual="$(sha256sum "$assets_dir/$asset" | awk '{print $1}')"
  if [[ -z "$expected" || "$expected" != "$actual" ]]; then
    echo "checksum mismatch for $asset" >&2
    exit 1
  fi
done

echo "embedded wechat-cli $version"
