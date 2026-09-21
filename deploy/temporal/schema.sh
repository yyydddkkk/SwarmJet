#!/bin/sh
set -eu
: "${POSTGRES_SEEDS:?required}"
: "${POSTGRES_USER:?required}"
: "${SQL_PASSWORD:?required}"
for database in temporal temporal_visibility; do
  temporal-sql-tool --plugin postgres12 --ep "$POSTGRES_SEEDS" -p 5432 -u "$POSTGRES_USER" --db "$database" setup-schema -v 0.0
  case "$database" in temporal) schema=temporal;; *) schema=visibility;; esac
  temporal-sql-tool --plugin postgres12 --ep "$POSTGRES_SEEDS" -p 5432 -u "$POSTGRES_USER" --db "$database" update-schema -d "/etc/temporal/schema/postgresql/v12/$schema/versioned"
done
