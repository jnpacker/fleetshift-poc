#!/usr/bin/env bash
set -euo pipefail

# ------------------------------------------------------------------
# FleetShift gRPC Route Certificate Deploy
#
# Installs cert-manager if needed, issues a trusted certificate for
# the existing FleetShift gRPC Route hostname, grants the OpenShift
# router serviceaccount access to the certificate Secret, and patches
# the Route to use spec.tls.externalCertificate.
#
# Traffic path:
#   client --TLS+ALPN h2--> Route --h2c--> fleetshift-server:50051
#
# Usage:
#   ./deploy.sh --acme-email you@example.com
# ------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
MANIFESTS_DIR="${WORKFLOW_DIR}/manifests"

NAMESPACE="fleetshift"
ROUTE_NAME="grpc"
ISSUER_NAME="fleetshift-grpc-letsencrypt-prod"
TLS_SECRET_NAME="fleetshift-grpc-route-tls"
BACKUP_NAMESPACE="cert-manager-operator"
ACME_EMAIL=""
ROUTE_HOST_OVERRIDE=""
FRESH_CERT="false"

info()  { echo "==> $*"; }
warn()  { echo "WARNING: $*"; }
error() { echo "ERROR: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage:
  ./deploy.sh --acme-email you@example.com [options]

Options:
  --acme-email <email>            ACME email address (required)
  --namespace <namespace>         FleetShift namespace (default: fleetshift)
  --route-name <name>             gRPC Route name (default: grpc)
  --issuer-name <name>            ClusterIssuer name (default: fleetshift-grpc-letsencrypt-prod)
  --tls-secret-name <name>        TLS Secret name (default: fleetshift-grpc-route-tls)
  --route-host <host>             Optional expected Route hostname override
  --fresh-cert                    Ignore any saved certificate backup and force fresh issuance
  -h, --help                      Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --acme-email) ACME_EMAIL="${2:-}"; shift 2 ;;
    --namespace) NAMESPACE="${2:-}"; shift 2 ;;
    --route-name) ROUTE_NAME="${2:-}"; shift 2 ;;
    --issuer-name) ISSUER_NAME="${2:-}"; shift 2 ;;
    --tls-secret-name) TLS_SECRET_NAME="${2:-}"; shift 2 ;;
    --route-host) ROUTE_HOST_OVERRIDE="${2:-}"; shift 2 ;;
    --fresh-cert) FRESH_CERT="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) error "Unknown argument: $1" ;;
  esac
done

[[ -n "${ACME_EMAIL}" ]] || error "--acme-email is required"

wait_for_csv() {
  local namespace="$1"
  local label="$2"
  local timeout="${3:-300}"
  local elapsed=0
  local phase=""

  while [[ $elapsed -lt $timeout ]]; do
    phase=$(oc get csv -n "$namespace" -l "$label" \
      -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
    if [[ "${phase}" == "Succeeded" ]]; then
      return 0
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done
  return 1
}

wait_for_deployment() {
  local namespace="$1"
  local name="$2"
  local timeout="${3:-180}"
  local elapsed=0
  local remaining="${timeout}"

  info "Waiting for deployment/${name} in namespace ${namespace}..."
  while [[ $elapsed -lt $timeout ]]; do
    if oc get deployment "${name}" -n "${namespace}" &>/dev/null; then
      remaining=$((timeout - elapsed))
      oc wait --for=condition=Available "deployment/${name}" -n "${namespace}" --timeout="${remaining}s"
      return 0
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  return 1
}

render_template() {
  local src="$1"
  local out="$2"

  sed \
    -e "s|__NAMESPACE__|${NAMESPACE}|g" \
    -e "s|__ISSUER_NAME__|${ISSUER_NAME}|g" \
    -e "s|__ACME_EMAIL__|${ACME_EMAIL}|g" \
    -e "s|__GRPC_ROUTE_HOST__|${GRPC_ROUTE_HOST}|g" \
    -e "s|__TLS_SECRET_NAME__|${TLS_SECRET_NAME}|g" \
    "${src}" > "${out}"
}

apply_template() {
  local src="$1"
  local tmp
  tmp="$(mktemp)"
  render_template "${src}" "${tmp}"
  oc apply -f "${tmp}"
  rm -f "${tmp}"
}

wait_for_cert_manager_webhook_trust() {
  local validation_manifest="$1"
  local timeout="${2:-180}"
  local elapsed=0
  local ca_bundle=""
  local tmp

  tmp="$(mktemp)"
  render_template "${validation_manifest}" "${tmp}"

  while [[ $elapsed -lt $timeout ]]; do
    ca_bundle="$(oc get validatingwebhookconfiguration cert-manager-webhook \
      -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)"

    if [[ -n "${ca_bundle}" ]] && oc apply --dry-run=server -f "${tmp}" >/dev/null 2>&1; then
      rm -f "${tmp}"
      return 0
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  rm -f "${tmp}"
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || error "'$1' CLI not found in PATH."
}

require_optional_command() {
  command -v "$1" >/dev/null 2>&1
}

cert_manager_subscription_exists() {
  oc get subscription openshift-cert-manager-operator -n cert-manager-operator &>/dev/null
}

backup_secret_name() {
  echo "${TLS_SECRET_NAME}-backup"
}

backup_tls_secret() {
  local backup_name
  backup_name="$(backup_secret_name)"

  if ! oc get secret "${TLS_SECRET_NAME}" -n "${NAMESPACE}" &>/dev/null; then
    warn "TLS Secret '${TLS_SECRET_NAME}' not found in namespace '${NAMESPACE}' — skipping backup."
    return 0
  fi

  if ! oc get namespace "${BACKUP_NAMESPACE}" &>/dev/null; then
    warn "Backup namespace '${BACKUP_NAMESPACE}' not found — skipping backup."
    return 0
  fi

  info "Backing up TLS Secret '${TLS_SECRET_NAME}' to '${BACKUP_NAMESPACE}/${backup_name}'..."
  oc get secret "${TLS_SECRET_NAME}" -n "${NAMESPACE}" -o json \
    | jq --arg name "${backup_name}" '
        del(.metadata.namespace, .metadata.resourceVersion, .metadata.uid,
            .metadata.creationTimestamp, .metadata.ownerReferences,
            .metadata.managedFields, .metadata.annotations."kubectl.kubernetes.io/last-applied-configuration") |
        .metadata.name = $name
      ' \
    | oc apply -n "${BACKUP_NAMESPACE}" -f -
}

restore_tls_secret_from_backup() {
  local backup_name
  backup_name="$(backup_secret_name)"

  if [[ "${FRESH_CERT}" == "true" ]]; then
    info "--fresh-cert requested; skipping backup restore."
    return 1
  fi

  if oc get secret "${TLS_SECRET_NAME}" -n "${NAMESPACE}" &>/dev/null; then
    info "TLS Secret '${TLS_SECRET_NAME}' already exists in namespace '${NAMESPACE}', skipping backup restore."
    return 0
  fi

  if ! oc get secret "${backup_name}" -n "${BACKUP_NAMESPACE}" &>/dev/null; then
    return 1
  fi

  info "Restoring TLS Secret from '${BACKUP_NAMESPACE}/${backup_name}'..."
  oc get secret "${backup_name}" -n "${BACKUP_NAMESPACE}" -o json \
    | jq --arg name "${TLS_SECRET_NAME}" '
        del(.metadata.namespace, .metadata.resourceVersion, .metadata.uid,
            .metadata.creationTimestamp, .metadata.ownerReferences,
            .metadata.managedFields, .metadata.annotations."kubectl.kubernetes.io/last-applied-configuration") |
        .metadata.name = $name
      ' \
    | oc apply -n "${NAMESPACE}" -f -
  return 0
}

require_can_i() {
  local verb="$1"
  local resource="$2"
  local namespace="${3:-}"

  if [[ -n "${namespace}" ]]; then
    oc auth can-i "${verb}" "${resource}" -n "${namespace}" >/dev/null 2>&1 || \
      error "Missing permission: cannot ${verb} ${resource} in namespace ${namespace}"
  else
    oc auth can-i "${verb}" "${resource}" >/dev/null 2>&1 || \
      error "Missing permission: cannot ${verb} ${resource}"
  fi
}

detect_route_host() {
  local host=""
  host="$(oc get route "${ROUTE_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.host}' 2>/dev/null || true)"
  if [[ -z "${host}" ]]; then
    host="$(oc get route "${ROUTE_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.ingress[0].host}' 2>/dev/null || true)"
  fi
  [[ -n "${host}" ]] || error "Could not determine host for route '${ROUTE_NAME}' in namespace '${NAMESPACE}'."
  echo "${host}"
}

ensure_ingress_http2() {
  local current=""
  current="$(oc get ingresscontrollers.operator.openshift.io/default -n openshift-ingress-operator \
    -o jsonpath='{.metadata.annotations.ingress\.operator\.openshift\.io/default-enable-http2}' 2>/dev/null || true)"

  if [[ "${current}" == "true" ]]; then
    info "Default ingress controller already has HTTP/2 enabled."
    return 0
  fi

  require_can_i patch ingresscontrollers.operator.openshift.io openshift-ingress-operator

  info "Enabling HTTP/2 on the default ingress controller..."
  oc -n openshift-ingress-operator annotate ingresscontrollers/default \
    ingress.operator.openshift.io/default-enable-http2=true --overwrite
}

verify_service_h2c() {
  local app_protocol
  app_protocol="$(oc get svc fleetshift-server -n "${NAMESPACE}" -o jsonpath='{.spec.ports[?(@.name=="grpc")].appProtocol}' 2>/dev/null || true)"
  [[ "${app_protocol}" == "kubernetes.io/h2c" ]] || \
    error "Service 'fleetshift-server' grpc port must have appProtocol=kubernetes.io/h2c before deploying Route certs."
}

patch_route_external_cert() {
  local patch
  patch="$(cat <<EOF
{"spec":{"tls":{"certificate":null,"key":null,"caCertificate":null,"externalCertificate":{"name":"${TLS_SECRET_NAME}"}}}}
EOF
)"
  oc patch route "${ROUTE_NAME}" -n "${NAMESPACE}" --type=merge -p "${patch}"
}

verify_route_patch() {
  local external_name
  external_name="$(oc get route "${ROUTE_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.tls.externalCertificate.name}' 2>/dev/null || true)"
  [[ "${external_name}" == "${TLS_SECRET_NAME}" ]] || \
    error "Route '${ROUTE_NAME}' was not patched to use externalCertificate=${TLS_SECRET_NAME}."
}

verify_alpn() {
  local output=""
  output="$(openssl s_client -alpn h2 -connect "${GRPC_ROUTE_HOST}:443" -servername "${GRPC_ROUTE_HOST}" </dev/null 2>/dev/null || true)"
  grep -q "ALPN protocol: h2" <<<"${output}" || \
    error "Route ${GRPC_ROUTE_HOST} is not negotiating ALPN h2 yet."
}

verify_http2() {
  local version
  version="$(curl --http2 -sS -o /dev/null -w '%{http_version}' "https://${GRPC_ROUTE_HOST}/" || true)"
  [[ "${version}" == "2" ]] || error "Route ${GRPC_ROUTE_HOST} did not negotiate HTTP/2 with curl."
}

verify_grpcurl() {
  require_optional_command grpcurl || {
    warn "'grpcurl' not found in PATH. Skipping grpcurl verification."
    return 0
  }
  grpcurl "${GRPC_ROUTE_HOST}:443" list >/dev/null
}

info "Checking prerequisites..."
require_command oc
require_command openssl
require_command curl
require_command jq
timeout 5 oc whoami &>/dev/null || error "Not logged in to OpenShift. Run 'oc login' first."

oc get route "${ROUTE_NAME}" -n "${NAMESPACE}" &>/dev/null || \
  error "Route '${ROUTE_NAME}' not found in namespace '${NAMESPACE}'. Deploy FleetShift first."
oc get svc fleetshift-server -n "${NAMESPACE}" &>/dev/null || \
  error "Service 'fleetshift-server' not found in namespace '${NAMESPACE}'. Deploy FleetShift first."

verify_service_h2c
ensure_ingress_http2

GRPC_ROUTE_HOST="$(detect_route_host)"
if [[ -n "${ROUTE_HOST_OVERRIDE}" && "${ROUTE_HOST_OVERRIDE}" != "${GRPC_ROUTE_HOST}" ]]; then
  error "--route-host (${ROUTE_HOST_OVERRIDE}) does not match the live Route host (${GRPC_ROUTE_HOST}). This workflow certificates the existing Route host only."
fi

info "gRPC Route host: ${GRPC_ROUTE_HOST}"

if ! cert_manager_subscription_exists; then
  require_can_i create namespaces
  require_can_i create subscriptions.operators.coreos.com cert-manager-operator
  require_can_i create operatorgroups.operators.coreos.com cert-manager-operator
fi

if ! cert_manager_subscription_exists; then
  info "Installing cert-manager operator..."
  oc apply -f "${MANIFESTS_DIR}/cert-manager-sub.yaml"
else
  info "cert-manager operator subscription already exists; ensuring cert-manager is ready."
fi

info "Waiting for cert-manager operator CSV..."
wait_for_csv "cert-manager-operator" \
  "operators.coreos.com/openshift-cert-manager-operator.cert-manager-operator" 300 || \
  error "Timed out waiting for cert-manager operator CSV."

wait_for_deployment cert-manager cert-manager 180 || \
  error "Timed out waiting for deployment/cert-manager in namespace cert-manager."
wait_for_deployment cert-manager cert-manager-webhook 180 || \
  error "Timed out waiting for deployment/cert-manager-webhook in namespace cert-manager."
wait_for_deployment cert-manager cert-manager-cainjector 180 || \
  error "Timed out waiting for deployment/cert-manager-cainjector in namespace cert-manager."

info "Waiting for cert-manager webhook CA injection and API trust..."
wait_for_cert_manager_webhook_trust "${MANIFESTS_DIR}/cluster-issuer.yaml" 180 || \
  error "Timed out waiting for cert-manager webhook trust to become ready."

info "Applying ClusterIssuer ${ISSUER_NAME}..."
apply_template "${MANIFESTS_DIR}/cluster-issuer.yaml"

info "Applying router Secret-reader RBAC..."
apply_template "${MANIFESTS_DIR}/router-secret-reader-role.yaml"
apply_template "${MANIFESTS_DIR}/router-secret-reader-binding.yaml"

restore_tls_secret_from_backup || true

info "Applying Certificate for ${GRPC_ROUTE_HOST}..."
apply_template "${MANIFESTS_DIR}/certificate.yaml"

info "Waiting for certificate to become Ready..."
oc wait --for=condition=Ready certificate/fleetshift-grpc-route -n "${NAMESPACE}" --timeout=600s || \
  error "Timed out waiting for Certificate/fleetshift-grpc-route to become Ready."

oc get secret "${TLS_SECRET_NAME}" -n "${NAMESPACE}" &>/dev/null || \
  error "Expected TLS Secret '${TLS_SECRET_NAME}' was not created."

info "Patching route '${ROUTE_NAME}' to use externalCertificate..."
patch_route_external_cert
verify_route_patch

info "Waiting briefly for router reconciliation..."
sleep 5

info "Verifying ALPN h2 on the Route..."
verify_alpn

info "Verifying HTTP/2 negotiation with curl..."
verify_http2

info "Verifying gRPC reflection with grpcurl..."
verify_grpcurl

backup_tls_secret

echo ""
echo "=== gRPC Route Certificate Ready ==="
echo "  Route host:      ${GRPC_ROUTE_HOST}"
echo "  TLS Secret:      ${TLS_SECRET_NAME}"
echo "  Backup Secret:   ${BACKUP_NAMESPACE}/$(backup_secret_name)"
echo "  Issuer:          ${ISSUER_NAME}"
echo ""
echo "  fleetctl usage:"
echo "    fleetctl --server ${GRPC_ROUTE_HOST}:443 --server-tls deployment list"
