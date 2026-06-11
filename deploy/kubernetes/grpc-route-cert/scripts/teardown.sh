#!/usr/bin/env bash
set -euo pipefail

# ------------------------------------------------------------------
# FleetShift gRPC Route Certificate Teardown
#
# Removes the cert-manager-backed certificate integration from the
# FleetShift gRPC Route and deletes the namespace-scoped certificate
# resources. Backs up the TLS certificate to avoid unnecessary ACME
# reissuance on redeploy, and deletes the FleetShift-specific issuer.
#
# Optionally uninstalls the cert-manager operator.
#
# Usage:
#   ./teardown.sh
# ------------------------------------------------------------------

NAMESPACE="fleetshift"
ROUTE_NAME="grpc"
ISSUER_NAME="fleetshift-grpc-letsencrypt-prod"
TLS_SECRET_NAME="fleetshift-grpc-route-tls"
BACKUP_NAMESPACE="cert-manager-operator"

info()  { echo "==> $*"; }
warn()  { echo "WARNING: $*"; }
error() { echo "ERROR: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage:
  ./teardown.sh [options]

Options:
  --namespace <namespace>         FleetShift namespace (default: fleetshift)
  --route-name <name>             gRPC Route name (default: grpc)
  --issuer-name <name>            ClusterIssuer name (default: fleetshift-grpc-letsencrypt-prod)
  --tls-secret-name <name>        TLS Secret name (default: fleetshift-grpc-route-tls)
  --backup-namespace <name>       Namespace that stores the backup Secret (default: cert-manager-operator)
  -h, --help                      Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace) NAMESPACE="${2:-}"; shift 2 ;;
    --route-name) ROUTE_NAME="${2:-}"; shift 2 ;;
    --issuer-name) ISSUER_NAME="${2:-}"; shift 2 ;;
    --tls-secret-name) TLS_SECRET_NAME="${2:-}"; shift 2 ;;
    --backup-namespace) BACKUP_NAMESPACE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) error "Unknown argument: $1" ;;
  esac
done

require_command() {
  command -v "$1" >/dev/null 2>&1 || error "'$1' CLI not found in PATH."
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

patch_route_remove_cert_integration() {
  local patch
  patch="$(cat <<'EOF'
{"spec":{"tls":{"externalCertificate":null,"certificate":null,"key":null,"caCertificate":null}}}
EOF
)"
  oc patch route "${ROUTE_NAME}" -n "${NAMESPACE}" --type=merge -p "${patch}"
}

info "Checking prerequisites..."
require_command oc
require_command jq
timeout 5 oc whoami &>/dev/null || error "Not logged in to OpenShift. Run 'oc login' first."

if oc get route "${ROUTE_NAME}" -n "${NAMESPACE}" &>/dev/null; then
  info "Removing Route certificate integration from ${NAMESPACE}/${ROUTE_NAME}..."
  patch_route_remove_cert_integration
else
  warn "Route '${ROUTE_NAME}' not found in namespace '${NAMESPACE}' — skipping Route patch."
fi

backup_tls_secret

info "Deleting namespace-scoped certificate resources..."
oc delete certificate fleetshift-grpc-route -n "${NAMESPACE}" --ignore-not-found=true
oc delete secret "${TLS_SECRET_NAME}" -n "${NAMESPACE}" --ignore-not-found=true
oc delete role fleetshift-grpc-route-cert-reader -n "${NAMESPACE}" --ignore-not-found=true
oc delete rolebinding fleetshift-grpc-route-cert-reader -n "${NAMESPACE}" --ignore-not-found=true

info "Deleting ClusterIssuer ${ISSUER_NAME}..."
oc delete clusterissuer "${ISSUER_NAME}" --ignore-not-found=true

echo ""
echo "Operators are shared cluster resources and slow to reinstall (~5 min each)."
echo "Skip unless you're fully decommissioning FleetShift Route certs from this cluster."
echo ""
read -rp "Uninstall cert-manager operator? (y/N): " remove_cm
if [[ "${remove_cm}" =~ ^[Yy]$ ]]; then
  warn "Uninstalling cert-manager will also remove the backup Secret stored in namespace '${BACKUP_NAMESPACE}'."
  info "Removing cert-manager operator..."
  oc delete subscription openshift-cert-manager-operator -n cert-manager-operator --ignore-not-found=true
  cm_csv=$(oc get csv -n cert-manager-operator \
    -l operators.coreos.com/openshift-cert-manager-operator.cert-manager-operator \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
  if [[ -n "${cm_csv}" ]]; then
    oc delete csv "${cm_csv}" -n cert-manager-operator --ignore-not-found=true
  fi
  oc delete namespace cert-manager --ignore-not-found=true
  oc delete namespace cert-manager-operator --ignore-not-found=true
  info "cert-manager operator removed."
else
  info "Leaving cert-manager installed."
fi

echo ""
echo "=== gRPC Route Certificate Teardown Complete ==="
echo "  Route certificate integration removed from ${NAMESPACE}/${ROUTE_NAME}"
echo "  Namespace-scoped certificate resources deleted"
echo "  Backup Secret stored at ${BACKUP_NAMESPACE}/$(backup_secret_name) unless cert-manager was uninstalled"
echo "  Ingress HTTP/2 annotation left unchanged"
