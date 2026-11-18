package secretstoretest

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

func TestFakeConforms(t *testing.T) {
	Run(t, func(*testing.T) secretstore.Store { return NewFake() })
}
