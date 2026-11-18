package connection

import (
	"strings"
	"testing"
)

func TestConfigResolveRefusesWhatCannotBeServed(t *testing.T) {
	lookup := func(k string) (string, bool) { return "s3cret", k == "SECRET" }
	oauth := &OAuth{ClientID: "c", ClientSecretEnv: "SECRET", RedirectURIs: []string{"https://consumer.example/cb"}}
	cases := map[string]Config{
		"allow names no instance":   {Allow: []Allow{{Port: "tracker", Kind: "clickup", Instance: "x", Methods: []string{"token"}}}},
		"oauth without application": {Allow: []Allow{{Port: "tracker", Kind: "clickup", Instance: "cu", Methods: []string{"oauth"}}}, Instances: map[string]Instance{"cu": {Kind: "clickup"}}},
		"unset client secret":       {Instances: map[string]Instance{"cu": {Kind: "clickup", OAuth: &OAuth{ClientID: "c", ClientSecretEnv: "MISSING", RedirectURIs: []string{"https://a.example/cb"}}}}},
		"http redirect":             {Instances: map[string]Instance{"cu": {Kind: "clickup", OAuth: &OAuth{ClientID: "c", ClientSecretEnv: "SECRET", RedirectURIs: []string{"http://a.example/cb"}}}}},
		"unknown method":            {Allow: []Allow{{Port: "forge", Kind: "gitlab", Instance: "gl", Methods: []string{"magic"}}}, Instances: map[string]Instance{"gl": {Kind: "gitlab", OAuth: oauth}}},
		"http token url":            {Instances: map[string]Instance{"cu": {Kind: "clickup", OAuth: &OAuth{ClientID: "c", ClientSecretEnv: "SECRET", RedirectURIs: []string{"https://a.example/cb"}, TokenURL: "http://a.example/token"}}}},
		"http authorize url":        {Instances: map[string]Instance{"cu": {Kind: "clickup", OAuth: &OAuth{ClientID: "c", ClientSecretEnv: "SECRET", RedirectURIs: []string{"https://a.example/cb"}, AuthorizeURL: "http://a.example/authorize"}}}},
		"bad port":                  {Allow: []Allow{{Port: "board", Kind: "gitlab", Instance: "gl", Methods: []string{"oauth"}}}, Instances: map[string]Instance{"gl": {Kind: "gitlab", OAuth: oauth}}},
	}
	for name, cfg := range cases {
		if _, err := cfg.Resolve(lookup); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := Config{Enabled: true, Allow: []Allow{{Port: "forge", Kind: "gitlab", Instance: "gl", Methods: []string{"oauth"}}}, Instances: map[string]Instance{"gl": {Kind: "gitlab", URL: "https://gitlab.example", OAuth: oauth}}}
	instances, err := good.Resolve(lookup)
	if err != nil || instances["gl"].OAuth.ClientSecret != "s3cret" || good.Max != DefaultMax {
		t.Fatalf("resolve = %+v, %v", instances, err)
	}
	if strings.Contains(instances["gl"].URL, "?") {
		t.Fatal("unexpected query")
	}
}
