#!/usr/bin/env bash
# Builds the gitops repo template into dist/template/ from template/ and
# examples/argocd/, for the badil version given as the only argument.
set -euo pipefail

if [ "$#" -ne 1 ] || ! [[ "$1" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi
version=$1

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/dist/template"
demo_repo='https://github\.com/badilhq/demo-gitops\.git'
placeholder='https://github.com/YOUR-ORG/YOUR-GITOPS-REPO.git'

rm -rf "$out"
mkdir -p "$out/previews" "$out/argocd"

sed "s/{{VERSION}}/$version/g" "$root/template/README.md" > "$out/README.md"
cp "$root/template/badil.yaml" "$out/badil.yaml"
: > "$out/previews/.gitkeep"
for f in appproject-previews.yaml applicationset-previews.yaml; do
  sed "s|$demo_repo|$placeholder|g" "$root/examples/argocd/$f" > "$out/argocd/$f"
done
