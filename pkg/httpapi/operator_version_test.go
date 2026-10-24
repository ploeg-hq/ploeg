package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/contracts"
)

func TestVersionReportsTheReleaseAndEveryServedContract(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Version: "0.2.0-rc.51", OperatorConfig: OperatorConfig{Consumers: consumers}}
	var body versionResponse
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "version"), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ploeg != "0.2.0-rc.51" || !reflect.DeepEqual(body.Contracts, contracts.Served()) {
		t.Fatalf("version answered %+v", body)
	}
}

func TestVersionNamesAnUnversionedBuildUnknown(t *testing.T) {
	consumers, token := operatorTestConsumers(t, nil, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	var body versionResponse
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "version"), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ploeg != unknownPloegVersion {
		t.Fatalf("an unversioned build answered %q", body.Ploeg)
	}
}

func TestVersionNeedsAnOperatorCredentialAndNoQuery(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Version: "0.2.0", OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		name, method, path, token string
		status                    int
	}{
		{"anonymous", "GET", "/api/v1/operator/version", "", 401},
		{"query", "GET", "/api/v1/operator/version?x=1", token, 400},
		{"write", "POST", "/api/v1/operator/version", token, 405},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
}
