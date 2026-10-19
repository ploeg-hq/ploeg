package store

import (
	"encoding/json"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The operator API renamed three fields in ADR-0093 and keeps emitting the
// old names beside the new ones for one release, so a consumer pinned to an
// older Ploeg release keeps working while it moves. Decoding accepts either
// name, so a client of this package also reads an older ploegd.

// MarshalJSON adds the deprecated leased count, equal to Active.
func (c OperatorWorkItemCounts) MarshalJSON() ([]byte, error) {
	type plain OperatorWorkItemCounts
	return json.Marshal(struct {
		plain
		Leased int64 `json:"leased"`
	}{plain(c), c.Active})
}

// UnmarshalJSON reads active, or the deprecated leased from an older ploegd.
func (c *OperatorWorkItemCounts) UnmarshalJSON(data []byte) error {
	type plain OperatorWorkItemCounts
	var v struct {
		plain
		Active *int64 `json:"active"`
		Leased *int64 `json:"leased"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*c = OperatorWorkItemCounts(v.plain)
	c.Active = firstSet(v.Active, v.Leased)
	return nil
}

// MarshalJSON adds the deprecated reservedUsd, equal to HeldUSD.
func (s OperatorSpend) MarshalJSON() ([]byte, error) {
	type plain OperatorSpend
	return json.Marshal(struct {
		plain
		ReservedUSD float64 `json:"reservedUsd"`
	}{plain(s), s.HeldUSD})
}

// UnmarshalJSON reads heldUsd, or the deprecated reservedUsd from an older
// ploegd.
func (s *OperatorSpend) UnmarshalJSON(data []byte) error {
	type plain OperatorSpend
	var v struct {
		plain
		HeldUSD     *float64 `json:"heldUsd"`
		ReservedUSD *float64 `json:"reservedUsd"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = OperatorSpend(v.plain)
	s.HeldUSD = firstSet(v.HeldUSD, v.ReservedUSD)
	return nil
}

// MarshalJSON adds the deprecated reservedUsd, equal to HeldUSD.
func (s OperatorShift) MarshalJSON() ([]byte, error) {
	type plain OperatorShift
	if s.CloseFailure == nil {
		if e, ok := work.ExplainClose(s.CloseReason); ok {
			s.CloseFailure = &e
		}
	}
	return json.Marshal(struct {
		plain
		ReservedUSD float64 `json:"reservedUsd"`
	}{plain(s), s.HeldUSD})
}

// UnmarshalJSON reads heldUsd, or the deprecated reservedUsd from an older
// ploegd.
func (s *OperatorShift) UnmarshalJSON(data []byte) error {
	type plain OperatorShift
	var v struct {
		plain
		HeldUSD     *float64 `json:"heldUsd"`
		ReservedUSD *float64 `json:"reservedUsd"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = OperatorShift(v.plain)
	s.HeldUSD = firstSet(v.HeldUSD, v.ReservedUSD)
	return nil
}

func firstSet[T any](values ...*T) T {
	for _, v := range values {
		if v != nil {
			return *v
		}
	}
	var zero T
	return zero
}
