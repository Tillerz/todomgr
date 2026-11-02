#!/usr/bin/env bash
# Publishes the archives in dist/ as a GitHub release for the Version in main.go.
# Order: ./build.sh, ./test.sh, try it yourself, git commit, then ./release.sh.
# Release text: docs/releases/v<Version>.md if it exists (GitHub adds its changelog link below it).
# Usage: ./release.sh   (needs: everything committed, a GitHub remote "origin", gh logged in)
set -euo pipefail
cd "$(dirname "$0")"

version=$(sed -n 's/^[[:space:]]*Version = "\(.*\)"$/\1/p' main.go)
[ -n "$version" ] || { echo "no Version in main.go"; exit 1; }
tag="v$version"

command -v gh >/dev/null || { echo "gh (GitHub CLI) is not installed"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "uncommitted changes, commit them first:"; git status --short; exit 1; }
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "tag $tag already exists: raise Version in main.go first"; exit 1
fi

# publish exactly what was built and tested (./build.sh, ./test.sh), nothing is rebuilt here
assets=(dist/todomgr-"$tag"-*.zip dist/todomgr-"$tag"-*.tar.gz dist/SHA256SUMS.txt)
for f in "${assets[@]}"; do
  [ -e "$f" ] || { echo "missing $f: run ./build.sh first"; exit 1; }
done
# docs/ (screenshots, release texts) is not part of the archives, so it may change after the build
newer=$(git ls-files | grep -v '^docs/' | while read -r f; do if [ "$f" -nt dist/SHA256SUMS.txt ]; then echo "$f"; fi; done)
[ -z "$newer" ] || { echo "changed after the last build, run ./build.sh and ./test.sh again:"; echo "$newer"; exit 1; }
echo "will publish:"; printf '  %s\n' "${assets[@]}"
notes=(--generate-notes)
if [ -f "docs/releases/$tag.md" ]; then
  notes+=(--notes-file "docs/releases/$tag.md")
  echo "release text: docs/releases/$tag.md, plus GitHub's changelog link"
else
  echo "release text: GitHub's automatic notes only (no docs/releases/$tag.md)"
fi

read -r -p "Publish $tag to GitHub? [y/N] " answer
[ "$answer" = "y" ] || { echo "stopped, nothing published"; exit 1; }

git tag -a "$tag" -m "$tag"
git push origin HEAD "$tag"
gh release create "$tag" --title "Tillerz Todo Mgr $tag" "${notes[@]}" --verify-tag "${assets[@]}"
