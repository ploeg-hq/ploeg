package store

import (
	"encoding/json"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func explainFailure(reason string) *work.FailureExplanation {
	e, ok := work.FailureReason(reason).Explain()
	if !ok {
		return nil
	}
	return &e
}

// MarshalJSON adds Failure, the plain explanation of the Run's failure
// reason (ADR-0130).
func (r OperatorRun) MarshalJSON() ([]byte, error) {
	type plain OperatorRun
	if r.Failure == nil && r.FailureReason != nil {
		r.Failure = explainFailure(*r.FailureReason)
	}
	return json.Marshal(plain(r))
}

// MarshalJSON adds Failure, the plain explanation of the Run's failure
// reason (ADR-0130).
func (r OperatorRunListItem) MarshalJSON() ([]byte, error) {
	type plain OperatorRunListItem
	if r.Failure == nil {
		r.Failure = explainFailure(r.FailureReason)
	}
	return json.Marshal(plain(r))
}
