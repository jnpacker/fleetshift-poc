package assisted

import (
	"context"

	"github.com/fleetshift/fleetshift-poc/fleetshift-server/internal/domain"
)

// Agent is a minimal [domain.DeliveryAgent] for the assisted addon.
// Real provisioning happens out-of-band: by the time a manifest
// reaches this agent, the wizard has already created the cluster,
// InfraEnv, and Discovery ISO directly against the real Assisted
// Service (see internal/transport/http/assisted_ocm_clusters.go).
// Deliver's only job is to acknowledge so the managed resource
// transitions to ACTIVE and becomes visible in FleetShift's cluster
// list immediately, matching the pattern documented in
// internal/infrastructure/sqlite/recording_delivery.go for addons
// with no in-band delivery work to perform.
type Agent struct {
	Reporter domain.DeliveryReporter
}

// NewAgent constructs an [Agent] that reports delivery outcomes
// through reporter.
func NewAgent(reporter domain.DeliveryReporter) *Agent {
	return &Agent{Reporter: reporter}
}

// Deliver immediately reports the delivery as delivered. There is no
// in-band provisioning work to perform: the Assisted Service cluster
// referenced by the manifest already exists by the time this is
// called.
func (a *Agent) Deliver(_ context.Context, _ domain.TargetInfo, deliveryID domain.DeliveryID, _ []domain.Manifest, _ domain.DeliveryAuth, _ *domain.Attestation, generation domain.Generation) error {
	go func() {
		_ = a.Reporter.ReportResult(context.Background(), deliveryID, generation, domain.DeliveryResult{
			State: domain.DeliveryStateDelivered,
		})
	}()
	return nil
}

// Remove immediately reports the removal as delivered. FleetShift does
// not delete the underlying Assisted Service cluster on managed
// resource deletion today -- that's follow-up work.
func (a *Agent) Remove(_ context.Context, _ domain.TargetInfo, deliveryID domain.DeliveryID, _ []domain.Manifest, _ domain.DeliveryAuth, _ *domain.Attestation, generation domain.Generation) error {
	go func() {
		_ = a.Reporter.ReportResult(context.Background(), deliveryID, generation, domain.DeliveryResult{
			State: domain.DeliveryStateDelivered,
		})
	}()
	return nil
}

var _ domain.DeliveryAgent = (*Agent)(nil)
