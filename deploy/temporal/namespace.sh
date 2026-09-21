#!/bin/sh
set -eu
: "${TEMPORAL_ADDRESS:?required}"
: "${TEMPORAL_NAMESPACE:?required}"
attempt=0
until temporal operator cluster health --address "$TEMPORAL_ADDRESS" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then echo 'Temporal health check timed out' >&2; exit 1; fi
  sleep 2
done
if ! temporal operator namespace describe --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" >/dev/null 2>&1; then
  temporal operator namespace create --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" --retention 168h
fi
