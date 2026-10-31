package provider

// Tracker capability names, as TrackerCapabilities reports them (ADR-0183).
const (
	CapabilityExecutionReader    = "execution_reader"
	CapabilityDeliveryIdentifier = "delivery_identifier"
	CapabilityBoardReader        = "board_reader"
	CapabilityTaskLinker         = "task_linker"
	CapabilityRelationReader     = "relation_reader"
	CapabilityDependencyReader   = "dependency_reader"
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
	return out
}
