#!/bin/sh
# Git invokes this executable directly; never echo credentials to diagnostic logs.
case "${1:-}" in
  *Username*|*username*)
    if [ -n "${GITHUB_TOKEN:-}" ]; then printf '%s\n' x-access-token; else printf '%s\n' oauth2; fi ;;
  *Password*|*password*) printf '%s\n' "${GITHUB_TOKEN:-${GITLAB_TOKEN:?Git token is required}}" ;;
  *) exit 1 ;;
esac
