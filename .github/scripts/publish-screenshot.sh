#!/usr/bin/env bash
# Publishes a PR screenshot: commits it to the "screenshots" branch (created
# as an orphan branch, with no history shared with main) as
# pr-<number>/<short sha>.png, then adds or updates one PR comment that
# shows it.
#
# Usage: publish-screenshot.sh <png>
# Environment: GH_TOKEN, REPO (owner/name), PR (number), SHA (head commit).
set -euo pipefail

png=$1
if command -v cygpath >/dev/null; then png=$(cygpath -u "$png"); fi
short=${SHA:0:7}
dest="pr-$PR/$short.png"
branch=screenshots

work=$(mktemp -d)
cd "$work"
git init -q
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git remote add origin "https://x-access-token:${GH_TOKEN}@github.com/${REPO}.git"

# Another PR's run may push at the same time; start again from the latest
# branch if the push is rejected.
for attempt in 1 2 3 4 5; do
  if git fetch -q --depth 1 origin "$branch" 2>/dev/null; then
    git checkout -q -f -B "$branch" FETCH_HEAD
  else
    git checkout -q --orphan "$branch"
    printf '# Screenshots\n\nPull request screenshots from the screenshot workflow. This branch shares no history with main; nothing else depends on it.\n' > README.md
    git add README.md
  fi
  mkdir -p "pr-$PR"
  cp "$png" "$dest"
  git add "$dest"
  git commit -qm "Screenshot for #$PR at $short" || true
  if git push -q origin "HEAD:refs/heads/$branch"; then
    break
  fi
  if [ "$attempt" = 5 ]; then
    echo "Couldn't push the screenshot" >&2
    exit 1
  fi
  echo "Push rejected (attempt $attempt); retrying"
  sleep $((attempt * 3))
done

url="https://raw.githubusercontent.com/$REPO/$branch/$dest"
marker='<!-- deckhand-screenshot -->'
body="$marker
### Screenshot

![deckhand at $short]($url)

Rendered from $short by the scripted scene in \`screenshot_e2e_test.go\`. This comment is updated on every push."

id=$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  --jq ".[] | select(.user.login == \"github-actions[bot]\" and (.body | startswith(\"$marker\"))) | .id" | head -n 1)
if [ -n "$id" ]; then
  gh api -X PATCH "repos/$REPO/issues/comments/$id" -f body="$body" >/dev/null
  echo "Updated comment $id"
else
  gh pr comment "$PR" --repo "$REPO" --body "$body"
fi
echo "Screenshot: $url"
