# Isolated kind Runner validation

This directory holds the first restricted Runner image, namespace policy, and
throwaway probes. The image runs `/runner`, reads `/workspace/input.txt` when
present (otherwise uses a fixed built-in input), atomically writes
`/workspace/results/<executionId>/<generation>.json`, and emits the same JSON
to stdout. The control plane must validate the result and execution right.

## Reproduction

Use a dedicated `KUBECONFIG` and a unique kind cluster name. Do not target an
existing cluster. `kind.yaml` disables kind's default CNI; install a CNI that
enforces NetworkPolicy before running untrusted code. The commands below used
kind v0.33.0, kubectl v1.35.0, and Cilium v1.20.2.

```sh
export KUBECONFIG=/tmp/swarmjet-runner-kubeconfig
kind create cluster --name sj-runner-probe --config deploy/k8s-runner/kind.yaml
helm install cilium oci://quay.io/cilium/charts/cilium --version 1.20.2 \
  --namespace kube-system --set ipam.mode=kubernetes --set image.pullPolicy=IfNotPresent
kubectl -n kube-system rollout status daemonset/cilium --timeout=180s
kubectl apply -f deploy/k8s-runner/namespace.yaml
kubectl apply -f deploy/k8s-runner/network-probe.yaml
kubectl -n swarmjet-runners wait --for=condition=Ready \
  pod/network-probe-server pod/network-probe-client --timeout=180s
```

Get the server Pod IP and run `wget -q -T 3 -O - http://<IP>:8080` inside
`network-probe-client`. It should print `allowed`. Then label the client
`app.kubernetes.io/name=swarmjet-runner`; after policy propagation, the same
request must time out. Apply `network-probe-allow.yaml` and repeat; it must
print `allowed` again. The positive control matters because a broken network
would also make a deny-only probe time out.

Build `deploy/k8s-runner/Dockerfile`, load the image into kind, and give the
Job its manifest-list digest (not the image config digest). When preloading
an image with kind, containerd may need a second image reference that names
the digest:

```sh
docker build -f deploy/k8s-runner/Dockerfile -t swarmjet-deterministic-runner:probe .
kind load docker-image swarmjet-deterministic-runner:probe --name sj-runner-probe
docker exec sj-runner-probe-control-plane ctr -n k8s.io images tag \
  docker.io/library/swarmjet-deterministic-runner:probe \
  docker.io/library/swarmjet-deterministic-runner@sha256:<manifest-list-digest>
kubectl apply -f deploy/k8s-runner/pvc-probe.yaml
go run ./deploy/k8s-runner/probe -image \
  swarmjet-deterministic-runner@sha256:<manifest-list-digest> > /tmp/swarmjet-runner-job.json
kubectl -n swarmjet-runners apply -f /tmp/swarmjet-runner-job.json
kubectl -n swarmjet-runners wait --for=condition=complete job/sj-live-probe-g1 --timeout=120s
kubectl -n swarmjet-runners logs job/sj-live-probe-g1
```

The `pvc-probe.yaml` claim uses 64Mi `ReadWriteOnce` storage. A second Pod mounting it should read the same JSON at
`/workspace/results/live-probe/1.json` as the Job log. Delete the entire
one-off cluster when finished: `kind delete cluster --name sj-runner-probe`.

## Observed on 2026-09-27

- kind node `v1.37.0` became Ready with Cilium `1.20.2`.
- The baseline HTTP probe returned `allowed`; the selected Runner probe timed
  out; temporary targeted egress restored `allowed`.
- The pinned image `swarmjet-deterministic-runner@sha256:53cc06d60d04a19b71950a450e6f4246a78114de154fc35faceb3e4ef05b04a7`
  completed Job `sj-live-probe-g1` with `backoffLimit: 0`.
- Job stdout and the independently mounted PVC file contained the same JSON,
  with `inputSha256` equal to
  `bee513a23275ffdd021418b1736cb0f5211a1992ffb3775bd5ed8547634f53b6`.

This verifies one-node Pod replacement and network policy behavior in kind.
It does not establish cross-node storage remount safety or node fencing.

## Real OpenCode exact-session probe

On 2026-09-27, a second disposable kind cluster used Cilium 1.20.2, the
`swarmjet-agent:local` image pinned to
`sha256:1f40f97ffbadaffff2318b28186daa02d43d49d65555b40f7e60a51a3cfd9c75`,
and a fresh `ReadWriteOnce` task PVC. A temporary `DEEPSEEK_API_KEY` Secret was
created from a local credential file, injected into the isolated Agent and
provider HTTP probes, then deleted before the cluster was deleted. No
credential value was placed in a manifest or host command line. The test policy
`opencode-network-probe.yaml` allowed DNS to CoreDNS and outbound TCP 443;
the base Runner default-deny policy remained active. This test policy is
intentionally broader than a production provider destination policy.

The Agent Jobs used separate Pods with a non-root user, read-only root
filesystem, no ServiceAccount token, dropped capabilities, resource limits,
and the same mounted PVC. `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, and
`XDG_CACHE_HOME` all pointed under `/workspace/home` on that PVC. `/tmp` was an
`emptyDir`. OpenCode v1.18.31 ran with `--format json --auto --pure --dir
/workspace -m deepseek/deepseek-flash`; the second run also passed `--session`
with the exact first-run ID. The model ID `deepseek/deepseek-chat` was rejected
by this OpenCode image's model catalog, despite a direct DeepSeek chat API
probe returning HTTP 200. The model-catalog error was visible only with
`--print-logs --log-level DEBUG`.

| Check | Observed value |
| --- | --- |
| First response | `marker stored.` |
| First session | `ses_f1e0b4730ffeEYac5Wld65FspW` |
| First Pod UID | `43a869c4-48d4-4e76-81d3-838cbfd1001e` |
| Old Job and Pod | Foreground deleted; Pod lookup returned empty before second Job |
| Second Pod UID | `f39cca72-6b0c-4b6c-97c6-b8f3f8646a85` |
| Second session | Same exact ID |
| Second response | `mango-cobalt-7421`, the marker supplied only in the first prompt |
| Second workspace | Same `sj-task-opencode-probe` PVC |

To repeat, create a fresh kind cluster with `kind.yaml`, install Cilium as
above, and apply `namespace.yaml` plus `opencode-network-probe.yaml`. Create
the Secret from a local credential file with
`kubectl -n swarmjet-runners create secret generic deepseek-session-probe
--from-file=DEEPSEEK_API_KEY="$DEEPSEEK_KEY_FILE"`. Create one task PVC. Run
the first restricted Job with the pinned Agent image and its Secret reference;
parse the `sessionID` from its JSON events. Foreground-delete the first Job,
wait until its Pod is absent, and start a second Job with the same PVC, profile
paths, model, and `--session <exact-id>`. Compare the JSON event session IDs
and text. Delete the Secret and disposable cluster after the probe. This
establishes exact-session continuity across two Pods on one kind node; it does
not establish cross-node storage remount or recovery safety after an uncertain
Pod termination.

## Trusted worker wiring

The trusted SwarmJet worker, not the Runner Pod, must use
`swarmjet-job-controller` in the Runner namespace. Set
`serviceAccountName: swarmjet-job-controller` in that worker Deployment and
leave its ServiceAccount token mounted so `NewInClusterController` can use the
cluster API and CA. If the worker runs in a different namespace, adjust the
RoleBinding subject accordingly; keep the Role scoped to the Runner namespace.
Runner Jobs use `swarmjet-runner`, whose token is explicitly disabled.

For the current offline vertical path, configure the trusted worker with
`KUBERNETES_EXECUTION_POOL`, `KUBERNETES_OFFLINE_VALIDATION=true`,
`KUBERNETES_VALIDATION_PROJECT_ID=<isolated fixture project ID>`,
`KUBERNETES_RUNNER_NAMESPACE=swarmjet-runners`, and
`KUBERNETES_RUNNER_IMAGE=<repository>@sha256:<digest>`. Resource and pool
settings are `KUBERNETES_POOL_CAPACITY`, `KUBERNETES_RUNNER_CPU_MILLI`,
`KUBERNETES_RUNNER_MEMORY_MI`, `KUBERNETES_RUNNER_STORAGE_MI`, and
`KUBERNETES_RUNNER_TIMEOUT_SECONDS`. Configure an isolated artifact store via
`KUBERNETES_ARTIFACT_BUCKET` and its provider settings, or
`KUBERNETES_ARTIFACT_ROOT` for local testing. PostgreSQL and Temporal must
also be isolated from live environments. The worker image and external service
endpoints are deployment-specific; none are embedded in these manifests.

References: [kind quick start](https://kind.sigs.k8s.io/docs/user/quick-start/),
[Cilium kind installation](https://docs.cilium.io/en/stable/installation/kind/),
[Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
