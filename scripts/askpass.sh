#!/bin/sh
# Git invokes this executable directly; never echo credentials to diagnostic logs.
case "${1:-}" in
  *Username*|*username*) printf '%s\n' oauth2 ;;
  *Password*|*password*) printf '%s\n' "${GITLAB_TOKEN:?GITLAB_TOKEN is required}" ;;
  *) exit 1 ;;
esac
