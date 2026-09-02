package assisted_test

import (
	"context"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/types/dynamicpb"

	assistedaddon "github.com/fleetshift/fleetshift-poc/fleetshift-server/internal/addon/assisted"
	"github.com/fleetshift/fleetshift-poc/fleetshift-server/internal/domain"
	"github.com/fleetshift/fleetshift-poc/fleetshift-server/internal/transport/dynamicapi"
)

func TestDescriptor_DeclaresCapabilities(t *testing.T) {
	d := assistedaddon.Descriptor()
	var hasDelivery, hasManaged bool
	for _, c := range d.Capabilities {
		switch c := c.(type) {
		case domain.DeliveryCapability:
			if c.TargetType == assistedaddon.TargetType {
				hasDelivery = true
			}
		case domain.ManagedResourceCapability:
			if c.ResourceType == assistedaddon.ClusterResourceType {
				hasManaged = true
			}
		}
	}
	if !hasDelivery {
		t.Error("missing DeliveryCapability for assisted target type")
	}
	if !hasManaged {
		t.Error("missing ManagedResourceCapability for Cluster")
	}
}

func TestSchema_UsesRegisteredSelfTarget(t *testing.T) {
	s := assistedaddon.Schema()
	if s.Management == nil {
		t.Fatal("Schema().Management is nil")
	}
	rst, ok := s.Management.Relation.(domain.RegisteredSelfTarget)
	if !ok {
		t.Fatalf("Relation type = %T, want RegisteredSelfTarget", s.Management.Relation)
	}
	if rst.ManifestType() != assistedaddon.ClusterManifestType {
		t.Errorf("ManifestType() = %q, want %q", rst.ManifestType(), assistedaddon.ClusterManifestType)
	}
}

// TestAssistedClusterSpec_AllFieldsOptional verifies the spec proto
// compiles and validates with zero fields set. The spec is populated
// server-side from the wizard's already-completed Assisted Service
// calls, not directly authored by an end user, so nothing needs to be
// buf.validate-required here.
func TestAssistedClusterSpec_AllFieldsOptional(t *testing.T) {
	schema := assistedaddon.Schema()
	desc, err := dynamicapi.CompileInline(context.Background(),
		schema.ProtoFiles, schema.EntryFile, schema.Management.SpecMessage)
	if err != nil {
		t.Fatalf("CompileInline: %v", err)
	}

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	msg := dynamicpb.NewMessage(desc.Message)
	if err := validator.Validate(msg); err != nil {
		t.Errorf("validate AssistedClusterSpec with no fields set: %v", err)
	}
}
