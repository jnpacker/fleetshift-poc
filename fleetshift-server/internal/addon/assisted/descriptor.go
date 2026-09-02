// Package assisted registers the Red Hat Assisted Installer Service as
// a FleetShift managed-resource provider for bare-metal OpenShift
// clusters. Unlike kind or gcphcp, this addon does not itself perform
// provisioning -- the browser-driven create-cluster wizard talks to
// the real Assisted Service directly through the OCM proxy (see
// internal/transport/http/assisted_ocm.go and
// assisted_ocm_clusters.go) to create the cluster, InfraEnv, and
// Discovery ISO. The addon's job is narrower: make the resulting
// cluster visible in FleetShift's own inventory the moment the wizard
// creates it, following the same managed-resource pattern as the kind
// and gcphcp addons (see docs/design/managed_resources.md).
//
// Real cluster lifecycle (host discovery, install progress, kubeconfig
// import once installation completes) is tracked out of band by
// polling the Assisted Service directly from the Hosts tab; wiring
// that back into this addon's own Fulfillment state is tracked as
// follow-up work under OME-273.
package assisted

import (
	_ "embed"

	"github.com/fleetshift/fleetshift-poc/fleetshift-server/internal/domain"
)

const specProtoPath = "addons/assisted/v1/assisted_cluster_spec.proto"

//go:embed assisted_cluster_spec.proto
var assistedClusterSpecProto string

// TargetType is the [domain.TargetType] for the assisted addon's own
// delivery target.
const TargetType domain.TargetType = "assisted"

// ClusterResourceType is the [domain.ResourceType] for Assisted
// Service bare-metal cluster managed resources.
const ClusterResourceType domain.ResourceType = "assisted.fleetshift.io/Cluster"

// ClusterManifestType is the [domain.ManifestType] for Assisted
// cluster manifests delivered to the assisted agent.
const ClusterManifestType domain.ManifestType = "api.assisted.cluster"

// AddonTargetID is the fixed target ID the assisted addon registers
// itself under, following the "<addon>-local" convention used by the
// kind addon ("kind-local").
const AddonTargetID domain.TargetID = "assisted-local"

// Descriptor returns the addon descriptor for the Assisted Installer
// Service provider. It declares a delivery capability for the addon's
// own (no-op) target and a managed resource capability for Assisted
// cluster registration.
func Descriptor() domain.AddonDescriptor {
	return domain.AddonDescriptor{
		ID:   "assisted.fleetshift.io",
		Name: "Assisted Installer Service Provider",
		Capabilities: []domain.Capability{
			domain.DeliveryCapability{TargetType: TargetType},
			domain.ManagedResourceCapability{ResourceType: ClusterResourceType},
		},
	}
}

// Schema returns the extension resource schema for the Assisted
// cluster resource type. It carries the proto definition and
// fulfillment relation that the platform uses to compile the dynamic
// API surface and route fulfillments to the assisted delivery agent.
func Schema() domain.ExtensionResourceSchema {
	return domain.ExtensionResourceSchema{
		ResourceType: ClusterResourceType,
		ProtoPackage: "assisted.fleetshift.v1",
		Version:      "v1",
		CollectionID: "clusters",
		Singular:     "Cluster",
		Plural:       "Clusters",
		ProtoFiles: map[string]string{
			specProtoPath: assistedClusterSpecProto,
		},
		EntryFile: specProtoPath,
		Management: &domain.ManagementSchema{
			SpecMessage: "addons.assisted.v1.AssistedClusterSpec",
			Relation:    domain.NewRegisteredSelfTarget(AddonTargetID, ClusterManifestType),
		},
	}
}
