# Host upgrade runner

`updater` runs beside the control API and beside each remote Node. Its HTTP port
is available only on the Compose network. The API calls the control updater at
`http://updater:8095`; the Node process proxies authenticated `/upgrade/*`
requests to its local updater. The updater keeps its journal outside the API and
Node containers, so their replacement does not interrupt an accepted operation.

The HTTP contract is deliberately small:

| Request | Body | Result |
| --- | --- | --- |
| `GET /healthz` | none | process liveness |
| `POST /v1/plan` | `{"image":"registry/platform@sha256:..."}` | private operation journal and ID |
| `POST /v1/preflight` | `{"image":"registry/platform@sha256:..."}` | pulls and validates target package, dependency policy, and backup space while services still run |
| `POST /v1/apply` | `{"operationId":"..."}` | `202`; upgrade continues in the sidecar |
| `GET /v1/status?operationId=...` | none | current step, completed steps, backup directory, error |
| `POST /v1/rollback` | `{"operationId":"..."}` | `202`; only safe before control database migration; Node rollback is available after its replacement |

All endpoints except `/healthz` require `Authorization: Bearer <UPDATER_SECRET>`.
Requests accept only fixed JSON fields and an image reference or a generated
operation ID. They cannot select a host path, command, or Compose service.
The control updater gets a separate random secret; the Node updater uses its
existing per Node secret. The host CLI supports the same `plan`, `apply`,
`status`, and `rollback` commands for recovery.

The control updater requires a platform image pinned by registry digest. It
stops writers, writes a `pg_dumpall` export, stops PostgreSQL, then archives its
`/var/lib/postgresql` volume, `DATA_DIR`, RabbitMQ, and the built in registry
into `UPDATER_STATE_DIR/backups/<operationId>`, and saves
SHA256 checksums in `manifest.json`. It checks free space before stopping
services. `UPDATER_STATE_DIR` must be a separate mounted filesystem from
`DATA_DIR`; the default path is only a mount target, not a preconfigured disk.
The target image carries the versioned Compose and service files in
`/opt/swarmjet-upgrade`; the runner stages them from the pulled image ID,
validates the release package version, then installs a fixed file list before
running schema migrations. Existing `.env` files and custom Compose override
are preserved. The journal records the previous running image under a private
local tag. Failed steps can be resumed; failures before schema migration bring
the previous services back automatically.

`release-policy.json` pins the current PostgreSQL, RabbitMQ, Temporal, registry,
gateway, and BuildKit image declarations. Policy schema 1 has no dependency
upgrade path: any change in those image declarations is rejected before the
control plane is stopped. The package also declares updater protocol version 1
and minimum Node protocol 2. A future dependency update needs an explicit
reviewed migration path and a new policy version. `nodeDataFormat: 1` is also
required before a Node is stopped; this version does not migrate Node data.

Once Temporal or application migration starts, the runner will not start old
images against changed databases. It reports `needsRestore=true` and keeps the
backup for an operator controlled restore. A Node has no local database
migration, so its previous image can be restarted directly. The API verifies
the Node's new heartbeat and image after the Node sidecar reports completion.

## Manual recovery after schema migration

Use this procedure only after the updater reports `needsRestore=true`. It
restores the whole control plane to the snapshot taken before migration. It
deletes the **current** contents of the four named data stores. Set `DEPLOY` to
the existing deployment directory and `BACKUP` to the failed operation's
`backupDir` before running the block as a Docker administrator. The original
PostgreSQL, RabbitMQ, and registry image tags must still be available locally;
the upgrade policy currently prevents changes to these dependency tags.

```bash
set -euo pipefail
DEPLOY=/absolute/path/to/deploy
BACKUP=/srv/swarmjet/upgrades/backups/REPLACE_WITH_OPERATION_ID
test -f "$BACKUP/manifest.json"
python3 - "$BACKUP" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'manifest.json').read_text())
required = {'postgres.sql', 'postgres.tar.gz', 'data.tar.gz', 'rabbitmq.tar.gz', 'registry.tar.gz'}
assert required <= manifest['sha256'].keys(), 'incomplete backup'
for name, expected in manifest['sha256'].items():
    digest = hashlib.sha256()
    with (root / name).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    assert digest.hexdigest() == expected, f'checksum mismatch: {name}'
PY

cd "$DEPLOY"
compose=(docker compose --env-file "$DEPLOY/.env" --env-file "$DEPLOY/.env.registry" \
  -f "$DEPLOY/compose.yaml" -f "$DEPLOY/compose.builder.yaml" -f "$DEPLOY/compose.registry.yaml")
if test -f "$DEPLOY/compose.override.yaml"; then compose+=(-f "$DEPLOY/compose.override.yaml"); fi
"${compose[@]}" stop api worker builder local-node temporal rabbitmq gateway registry postgres

service_volume() {
  local container
  container=$("${compose[@]}" ps -a -q "$1")
  test -n "$container"
  docker inspect --format '{{json .Mounts}}' "$container" | python3 -c \
    'import json,sys; target=sys.argv[1]; mounts=json.load(sys.stdin); print(next(m["Name"] for m in mounts if m["Type"]=="volume" and m["Destination"]==target))' "$2"
}
PG_VOLUME=$(service_volume postgres /var/lib/postgresql)
MQ_VOLUME=$(service_volume rabbitmq /var/lib/rabbitmq)
REGISTRY_VOLUME=$(service_volume registry /var/lib/registry)

cp -a "$BACKUP/.env" "$BACKUP/.env.registry" "$BACKUP"/compose*.yaml "$DEPLOY/"
for directory in temporal observability registry; do
  if test -d "$BACKUP/$directory"; then cp -a "$BACKUP/$directory/." "$DEPLOY/$directory/"; fi
done
for file in release-policy.json Dockerfile.updater upgrade_host.py install_node.py; do
  if test -f "$BACKUP/$file"; then cp -a "$BACKUP/$file" "$DEPLOY/$file"; fi
done

DATA_DIR=$(python3 - "$DEPLOY/.env" <<'PY'
import pathlib, sys
for line in pathlib.Path(sys.argv[1]).read_text().splitlines():
    if line.startswith('DATA_DIR='):
        print(line.split('=', 1)[1]); break
else:
    raise SystemExit('DATA_DIR missing')
PY
)
python3 - "$BACKUP" "$DATA_DIR" <<'PY'
import json, pathlib, sys
manifest = json.loads((pathlib.Path(sys.argv[1]) / 'manifest.json').read_text())
assert manifest['dataDir'] == sys.argv[2], 'DATA_DIR changed; restore its original absolute path'
PY
PINNED_IMAGE=$(python3 - "$BACKUP" <<'PY'
import json, pathlib, sys
print(json.loads((pathlib.Path(sys.argv[1]) / 'manifest.json').read_text())['pinnedImage'])
PY
)
docker image inspect "$PINNED_IMAGE" >/dev/null
export IMAGE="$PINNED_IMAGE"

restore_archive() {
  local destination=$1 archive=$2
  docker run --rm --network none --user 0:0 \
    -v "$destination:/restore" -v "$BACKUP:/backup:ro" \
    --entrypoint sh postgres:18.1-alpine3.23 -ec \
    'for item in /restore/* /restore/.[!.]* /restore/..?*; do
       if [ -e "$item" ] || [ -L "$item" ]; then rm -rf -- "$item"; fi
     done
     tar -xzf "/backup/$1.tar.gz" -C /restore --strip-components=1' restore "$archive"
}
restore_archive "$PG_VOLUME" postgres
restore_archive "$MQ_VOLUME" rabbitmq
restore_archive "$REGISTRY_VOLUME" registry
restore_archive "$DATA_DIR" data

# Start the restored database alone, then remove the stale maintenance gate.
"${compose[@]}" up -d --wait postgres
"${compose[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -U swarmjet -d swarmjet -c \
  "UPDATE platform_upgrades SET active=false, maintenance=false, status='failed' WHERE active OR maintenance"
"${compose[@]}" up -d --wait rabbitmq registry gateway temporal api worker builder buildkit local-node temporal-ui
"${compose[@]}" exec -T api swarmjet doctor
```

The physical PostgreSQL archive avoids replaying `pg_dumpall` into a newly
initialized Compose cluster that already has the `swarmjet` role and databases.
Keep `postgres.sql` as a second, inspectable export. Verify API readiness,
Temporal histories, RabbitMQ queues, registry images, and Node connectivity
before allowing new tasks. Persist the pinned old `IMAGE` reference in `.env`
before the next routine Compose operation, or it may select the failed target.
Do not treat this snapshot as a substitute for regular off-host backups.
