#!/usr/bin/env bash
# Validate the public Kubernetes CronJob contract against two isolated vcsim
# Services. The caller creates the kind cluster and loads both local images.
set -Eeuo pipefail

namespace="${VSFLEET_K8S_NAMESPACE:-vsfleet-e2e}"
debug_dir="${VSFLEET_K8S_DEBUG_DIR:-$(mktemp -d)}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$debug_dir"

collect_debug() {
  kubectl -n "$namespace" get all,configmap,secret,pvc,endpointslices -o yaml >"$debug_dir/resources.yaml" 2>&1 || true
  kubectl -n "$namespace" get events --sort-by=.lastTimestamp >"$debug_dir/events.txt" 2>&1 || true
  while IFS= read -r pod; do
    kubectl -n "$namespace" describe pod "$pod" >"$debug_dir/$pod.describe.txt" 2>&1 || true
    kubectl -n "$namespace" logs "$pod" --all-containers --prefix >"$debug_dir/$pod.log" 2>&1 || true
  done < <(kubectl -n "$namespace" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
}
trap collect_debug EXIT

run_job() {
  local name="$1"
  kubectl -n "$namespace" create job --from=cronjob/vsfleet-assessment "$name"
}

job_log() {
  kubectl -n "$namespace" logs "job/$1"
}

job_json() {
  # Kubernetes combines stdout and stderr. A partial assessment's expected
  # diagnostics can therefore land before or inside its JSON result. Keep them
  # in the pod logs, but remove just those known forms for the JSON assertions.
  job_log "$1" | awk '
    /^✕ / || /^vsfleet: / { next }
    /^[[:space:]]*(\{|\[)/ { json = 1 }
    json { print }
  '
}

wait_complete() {
  local name="$1"
  local failed
  for _ in {1..90}; do
    if kubectl -n "$namespace" get "job/$name" -o json | jq -e 'any(.status.conditions[]?; .type == "Complete" and .status == "True")' >/dev/null; then
      return 0
    fi
    failed="$(kubectl -n "$namespace" get "job/$name" -o json | jq '[.status.conditions[]? | select(.type == "Failed" and .status == "True")] | length')"
    if [[ "$failed" != "0" ]]; then
      kubectl -n "$namespace" describe "job/$name" >&2 || true
      kubectl -n "$namespace" logs "job/$name" >&2 || true
      return 1
    fi
    sleep 2
  done
  printf 'job %s did not complete within 3m\n' "$name" >&2
  return 1
}

wait_failed() {
  kubectl -n "$namespace" wait --for=condition=failed "job/$1" --timeout=3m
}

# A rolled-out Deployment can still be unreachable through its Service until
# the EndpointSlice marks the pod ready; until then kube-proxy refuses
# connections to the Service IP.
wait_for_ready_endpoints() {
  local service="$1"
  local ready=0
  for _ in {1..60}; do
    ready="$(kubectl -n "$namespace" get endpointslices -l "kubernetes.io/service-name=$service" -o json | jq '[.items[].endpoints[]? | select(.conditions.ready == true)] | length')"
    if [[ "$ready" != "0" ]]; then
      return 0
    fi
    sleep 2
  done
  printf 'service %s has no ready endpoint after 2m\n' "$service" >&2
  return 1
}

wait_for_no_ready_endpoints() {
  local service="$1"
  local ready=1
  for _ in {1..30}; do
    ready="$(kubectl -n "$namespace" get endpointslices -l "kubernetes.io/service-name=$service" -o json | jq '[.items[].endpoints[]? | select(.conditions.ready == true)] | length')"
    if [[ "$ready" == "0" ]]; then
      return 0
    fi
    sleep 2
  done
  printf 'service %s still has %s ready endpoint(s) after scale-down\n' "$service" "$ready" >&2
  return 1
}

# A ready EndpointSlice still races kube-proxy: until the node programs the
# Service rules, connections to the Service IP are refused. Gate the first
# assessment on both vcsim Services answering from inside the cluster.
wait_for_reachable_vcenters() {
  local args attempt
  args="$(kubectl -n "$namespace" get cronjob vsfleet-assessment -o json | jq -c '.spec.jobTemplate.spec.template.spec.containers[] | select(.name == "vsfleet") | .args')"
  kubectl -n "$namespace" patch cronjob vsfleet-assessment --type strategic -p '{"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"name":"vsfleet","args":["--timeout","5s","--all-contexts","-o","json","status"]}]}}}}}}'
  for attempt in {1..15}; do
    run_job "vsfleet-reachable-$attempt" >/dev/null
    if wait_complete "vsfleet-reachable-$attempt" 2>/dev/null &&
      job_json "vsfleet-reachable-$attempt" | jq -e 'length == 2 and all(.[]; .state == "connected")' >/dev/null; then
      kubectl -n "$namespace" patch cronjob vsfleet-assessment --type strategic -p "{\"spec\":{\"jobTemplate\":{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"vsfleet\",\"args\":$args}]}}}}}}"
      return 0
    fi
    sleep 2
  done
  printf 'vcsim Services were not reachable from the cluster after %s attempts\n' "$attempt" >&2
  return 1
}

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$repo_root/tests/kubernetes/storage-pv.yaml"
kubectl -n "$namespace" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: vsfleet-data
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 64Mi
  storageClassName: ""
  volumeName: vsfleet-e2e-history
EOF
kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Bound pvc/vsfleet-data --timeout=90s
kubectl -n "$namespace" create configmap vsfleet-config --from-file=config.toml="$repo_root/tests/kubernetes/config.toml"
kubectl -n "$namespace" create secret generic vsfleet-credentials --from-literal=vsfleet-vcsim=vsfleet-vcsim-password
kubectl -n "$namespace" apply -f "$repo_root/tests/kubernetes/vcsim.yaml"
kubectl -n "$namespace" rollout status deployment/vsfleet-vcsim-prod --timeout=2m
kubectl -n "$namespace" rollout status deployment/vsfleet-vcsim-edge --timeout=2m
wait_for_ready_endpoints vsfleet-vcsim-prod
wait_for_ready_endpoints vsfleet-vcsim-edge

kubectl -n "$namespace" apply -f "$repo_root/deploy/kubernetes/cronjob.yaml"
kubectl -n "$namespace" patch cronjob vsfleet-assessment --type merge -p '{"spec":{"suspend":true}}'
kubectl -n "$namespace" set image cronjob/vsfleet-assessment vsfleet=vsfleet:ci-test

# The checked-in workload must retain its non-root, read-only security contract.
kubectl -n "$namespace" get cronjob vsfleet-assessment -o json | jq -e '
  .spec.jobTemplate.spec.template.spec as $pod |
  ($pod.securityContext.fsGroup == 65532) and
  any($pod.containers[];
    .name == "vsfleet" and
    .securityContext.runAsNonRoot == true and
    .securityContext.readOnlyRootFilesystem == true and
    .securityContext.allowPrivilegeEscalation == false and
    (.securityContext.capabilities.drop | index("ALL") != null)
  )
' >/dev/null

# Keep failed pods for the CI collector. Production retains the checked-in
# OnFailure retry policy; this ephemeral assertion job should expose its first
# failure immediately.
kubectl -n "$namespace" patch cronjob vsfleet-assessment --type merge -p '{"spec":{"jobTemplate":{"spec":{"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never"}}}}}}'

wait_for_reachable_vcenters
run_job vsfleet-complete
wait_complete vsfleet-complete
job_json vsfleet-complete | jq -e '.status == "complete" and .requested_contexts == 2 and .successful_contexts == 2' >/dev/null

kubectl -n "$namespace" patch cronjob vsfleet-assessment --type strategic -p '{"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"name":"vsfleet","args":["-o","json","assessment","list"]}]}}}}}}'
run_job vsfleet-history
wait_complete vsfleet-history
job_json vsfleet-history | jq -e 'length >= 1 and any(.[]; .status == "complete")' >/dev/null

kubectl -n "$namespace" scale deployment/vsfleet-vcsim-edge --replicas=0
kubectl -n "$namespace" rollout status deployment/vsfleet-vcsim-edge --timeout=90s
wait_for_no_ready_endpoints vsfleet-vcsim-edge
kubectl -n "$namespace" patch cronjob vsfleet-assessment --type strategic -p '{"spec":{"jobTemplate":{"spec":{"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"vsfleet","args":["--timeout","5s","--all-contexts","-o","json","assessment","run","--fail-on-partial"]}]}}}}}}'
run_job vsfleet-partial
wait_failed vsfleet-partial

partial_pod="$(kubectl -n "$namespace" get pods -l job-name=vsfleet-partial -o jsonpath='{.items[0].metadata.name}')"
kubectl -n "$namespace" get pod "$partial_pod" -o json | jq -e '
  [.status.containerStatuses[] | select(.name == "vsfleet") | .state.terminated.exitCode] | index(3)
' >/dev/null
job_json vsfleet-partial | jq -e '.status == "partial" and .requested_contexts == 2 and .successful_contexts == 1' >/dev/null

kubectl -n "$namespace" patch cronjob vsfleet-assessment --type strategic -p '{"spec":{"jobTemplate":{"spec":{"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"vsfleet","args":["-o","json","assessment","readiness","latest"]}]}}}}}}'
run_job vsfleet-readiness
wait_complete vsfleet-readiness
job_json vsfleet-readiness | jq -e '.verdict != "ready" and (.unresolved | length) > 0' >/dev/null

printf 'Kubernetes end-to-end checks passed; debug output: %s\n' "$debug_dir"
