package worker

import (
	"fmt"
	"os"
)

const serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// RefuseServiceAccountToken reports a ServiceAccount token mounted into this
// container, which a Run pod behind the credential gateway must not have
// (ADR-0197). The chart refuses to render a Secret or projected volume into
// a Run pod; this catches a pod rendered some other way.
func RefuseServiceAccountToken() error {
	return refuseServiceAccountToken(serviceAccountTokenPath)
}

func refuseServiceAccountToken(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("PLOEG_CREDENTIAL_GATEWAY: a ServiceAccount token is mounted at %s; a Run pod behind the credential gateway sets automountServiceAccountToken: false", path)
	}
	return nil
}
