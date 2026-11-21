#!/bin/sh
# Propose the next release version from the pull requests merged into
# main since the last release tag, and print the commands that tag the
# next release candidate and promote it.
#
# Each pull request's branch prefix decides the bump: major/ bumps the major
# version, feat/ the minor version, fix/ the patch version; build/, ignore/,
# dependabot/, and other branches do not bump, and with no bump there is
# nothing to release (exit 1). Pull requests are squash
# merged, so the branch is looked up with the GitHub CLI (gh) from the
# "(#N)" at the end of each commit subject. "Merge pull request #N from
# owner/branch" subjects are read directly.
#
# The printed ^{commit} ref is single-quoted for shells that glob ^ and {}.
#
# Usage: scripts/next-version.sh [ref]   (default ref: origin/main)
set -eu

ref=${1:-origin/main}
git rev-parse --verify --quiet "${ref}^{commit}" > /dev/null || {
	echo "next-version: unknown ref ${ref}" >&2
	exit 2
}

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' "$ref" 2> /dev/null || true)
if [ -n "$last" ]; then
	range="${last}..${ref}"
	base=${last#v}
else
	range=$ref
	base=0.0.0
fi

subjects=$(git log --first-parent --format=%s "$range")
branches=$(printf '%s\n' "$subjects" | sed -n 's|^Merge pull request #[0-9][0-9]* from [^/][^/]*/||p')
numbers=$(printf '%s\n' "$subjects" | grep -v '^Merge pull request #' | sed -n 's/.*(#\([0-9][0-9]*\))$/\1/p')
if [ -n "$numbers" ]; then
	command -v gh > /dev/null 2>&1 || {
		echo "next-version: the GitHub CLI (gh) is needed to read squash-merged pull requests" >&2
		exit 2
	}
	for n in $numbers; do
		b=$(gh pr view "$n" --json headRefName --jq .headRefName) || {
			echo "next-version: cannot read pull request #${n} with gh" >&2
			exit 2
		}
		branches=$(printf '%s\n%s' "$branches" "$b")
	done
fi

bump="none"
for b in $branches; do
	case "$b" in
		major/*) bump="major" ;;
		feat/*) [ "$bump" = major ] || bump="minor" ;;
		fix/*) if [ "$bump" = none ]; then bump="patch"; fi ;;
	esac
done

sha=$(git rev-parse "$ref")

if [ "$bump" = none ]; then
	echo "next-version: no fix/, feat/, or major/ pull request merged since ${last:-the first commit}" >&2
	exit 1
fi

major=$(echo "$base" | cut -d. -f1)
minor=$(echo "$base" | cut -d. -f2)
patch=$(echo "$base" | cut -d. -f3)
case "$bump" in
	major) major=$((major + 1)); minor=0; patch=0 ;;
	minor) minor=$((minor + 1)); patch=0 ;;
	patch) patch=$((patch + 1)) ;;
esac
next="v${major}.${minor}.${patch}"

rc=1
while git rev-parse --verify --quiet "refs/tags/${next}-rc.${rc}" > /dev/null; do
	rc=$((rc + 1))
done

echo "Last release:     ${last:-none}"
echo "Merged branches:  $(echo "$branches" | tr '\n' ' ')"
echo "Bump:             ${bump}"
echo "Next version:     ${next}"
echo
echo "Tag the release candidate:"
echo "  git tag -s ${next}-rc.${rc} -m ${next}-rc.${rc} ${sha}"
echo "  git push origin ${next}-rc.${rc}"
echo
echo "After the release candidate is proven, promote it:"
echo "  git tag -s ${next} -m ${next} '${next}-rc.${rc}^{commit}'"
echo "  git push origin ${next}"
