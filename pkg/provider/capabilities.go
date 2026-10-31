package provider

// Tracker capability names, as TrackerCapabilities reports them (ADR-0183).
const (
	CapabilityExecutionReader    = "execution_reader"
	CapabilityDeliveryIdentifier = "delivery_identifier"
	CapabilityBoardReader        = "board_reader"
	CapabilityTaskLinker         = "task_linker"
	CapabilityRelationReader     = "relation_reader"
	CapabilityDependencyReader   = "dependency_reader"
	CapabilityWebhookChecker     = "webhook_checker"
	CapabilityWebhookRegistrar   = "webhook_registrar"
	CapabilityReferenceNamer     = "reference_namer"
)

// TrackerCapabilities names the optional tracker port interfaces t
// implements, in a fixed order. An adapter declares a capability by
// implementing its interface; this is how logs, connection reports and the
// conformance suite see the declaration.
func TrackerCapabilities(t TrackerProvider) []string {
	out := []string{}
	if _, ok := t.(ExecutionReader); ok {
		out = append(out, CapabilityExecutionReader)
	}
	if _, ok := t.(DeliveryIdentifier); ok {
		out = append(out, CapabilityDeliveryIdentifier)
	}
	if _, ok := t.(BoardReader); ok {
		out = append(out, CapabilityBoardReader)
	}
	if _, ok := t.(TaskLinker); ok {
		out = append(out, CapabilityTaskLinker)
	}
	if _, ok := t.(RelationReader); ok {
		out = append(out, CapabilityRelationReader)
	}
	if _, ok := t.(DependencyReader); ok {
		out = append(out, CapabilityDependencyReader)
	}
	if _, ok := t.(WebhookChecker); ok {
		out = append(out, CapabilityWebhookChecker)
	}
	if _, ok := t.(WebhookRegistrar); ok {
		out = append(out, CapabilityWebhookRegistrar)
	}
	if _, ok := t.(ReferenceNamer); ok {
		out = append(out, CapabilityReferenceNamer)
	}
	return out
}

// ReferenceNamer is implemented by a TrackerProvider whose items have a
// reference format of their own, the one its forge integrations recognise.
// A binary registers it with work.RegisterReferenceNamer so that branches
// and commit trailers use it (ADR-0183).
type ReferenceNamer interface {
	Reference(externalID string) string
}
