#!/usr/bin/env bash
# Applies the branch protection rules of `main` through the GitHub API, so the
# settings are written down and can be re-applied (it is idempotent).
#
#   scripts/protect-main.sh [OWNER/REPO]       # needs `gh` signed in as a repository admin
#
# Rules:
#   - changes reach main only through a pull request (no direct pushes, also not
#     for administrators); no approving review is required, so a sole maintainer
#     can merge their own pull request
#   - the `CI` status check (.github/workflows/ci.yml) must pass, on a branch that
#     is up to date with main
#   - review conversations must be resolved before merging
#   - force pushes and deleting main are refused
set -euo pipefail

repo="${1:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"
branch="main"

gh api --method PUT "repos/$repo/branches/$branch/protection" --input - > /dev/null <<'JSON'
{
  "required_status_checks": { "strict": true, "contexts": ["CI"] },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "required_approving_review_count": 0,
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false
  },
  "restrictions": null,
  "required_linear_history": false,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true
}
JSON

echo "$repo: branch '$branch' is protected:"
gh api "repos/$repo/branches/$branch/protection" --jq '
  "  pull request required (approvals needed: \(.required_pull_request_reviews.required_approving_review_count))",
  "  required checks: \(.required_status_checks.contexts | join(", ")) (up to date with main: \(.required_status_checks.strict))",
  "  applies to administrators: \(.enforce_admins.enabled)",
  "  force pushes allowed: \(.allow_force_pushes.enabled), deletion allowed: \(.allow_deletions.enabled)",
  "  conversation resolution required: \(.required_conversation_resolution.enabled)"'
