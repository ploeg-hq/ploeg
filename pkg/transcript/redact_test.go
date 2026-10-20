package transcript

import (
	"strings"
	"testing"
)

type redactionCase struct {
	name   string
	input  string
	secret string
	keep   string
}

var redactionCases = []redactionCase{
	{"pem private key", "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEsecretbody\n-----END RSA PRIVATE KEY-----\ndone", "MIIEsecretbody", "done"},
	{"unterminated pem private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA", "b3BlbnNzaC1rZXktdjEAAAA", ""},
	{"48-hex run token", "run 0123456789abcdef0123456789abcdef0123456789abcdef claimed", "0123456789abcdef0123456789abcdef0123456789abcdef", "claimed"},
	{"sk- key", "OPENAI_API_KEY is sk-proj-abcdefgh1234 now", "sk-proj-abcdefgh1234", "now"},
	{"upper-case SK- key", "key SK-ABCDEFGH1234 now", "SK-ABCDEFGH1234", "now"},
	{"bearer value", "Authorization: Bearer abcdefghijklmnop", "abcdefghijklmnop", "Authorization"},
	{"lower-case bearer value", "authorization: bearer abcdefghijklmnop", "abcdefghijklmnop", "authorization"},
	{"github classic token", "push with ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_abcdefghijklmnopqrstuvwxyz0123", "push with"},
	{"github app token", "token ghs_ABCDEFGHIJKLMNOPQRSTUVWX ok", "ghs_ABCDEFGHIJKLMNOPQRSTUVWX", "ok"},
	{"github fine-grained token", "github_pat_11ABCDEFG0123456789_abcdefghij", "github_pat_11ABCDEFG0123456789_abcdefghij", ""},
	{"gitlab token", "glpat-abcdefghij0123456789 used", "glpat-abcdefghij0123456789", "used"},
	{"aws access key id", "aws AKIAIOSFODNN7EXAMPLE configured", "AKIAIOSFODNN7EXAMPLE", "configured"},
	{"aws session key id", "aws ASIAIOSFODNN7EXAMPLE configured", "ASIAIOSFODNN7EXAMPLE", "configured"},
	{"slack token", "slack xoxb-1234567890-abcdefghij posted", "xoxb-1234567890-abcdefghij", "posted"},
	{"jwt", "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U end", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "end"},
	{"url userinfo", "git clone https://user:pa55word@forge.example/repo.git", "pa55word", "forge.example/repo.git"},
	{"password assignment", "password=hunter2hunter2", "hunter2hunter2", "password="},
	{"quoted secret in json", `{"client_secret": "s3cr3tv4lu3x"}`, "s3cr3tv4lu3x", "client_secret"},
	{"api key header", "curl -H 'x-api-key: a1a1a1a1a1a1a1a1'", "a1a1a1a1a1a1a1a1", "curl"},
	{"upper-case token assignment", "GITHUB_TOKEN=abcdefgh12345678", "abcdefgh12345678", "GITHUB_TOKEN="},
	{"long s in a case-folded keyword", "pa\u017fsword=hunter2hunter2", "hunter2hunter2", "word="},
	{"kelvin sign in a case-folded keyword", "to\u212aen: abcdefgh12345678", "abcdefgh12345678", "en:"},
	{"npmrc _auth", "//registry.npmjs.org/:_auth=dXNlcjpwYXNzd29yZA==\nalways-auth=true", "dXNlcjpwYXNzd29yZA==", "always-auth=true"},
	{"npmrc auth", "auth = dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA==", "auth = "},
	{"npm auth environment variable", "NPM_CONFIG__AUTH=dXNlcjpwYXNzd29yZA== npm publish", "dXNlcjpwYXNzd29yZA==", "npm publish"},
	{"npmrc authToken", "//registry.npmjs.org/:_authToken=npm_abcdefghijklmnop", "npm_abcdefghijklmnop", "registry.npmjs.org"},
	{"stripe live secret key", "STRIPE=sk_live_51Habcdefghijklmnop", "sk_live_51Habcdefghijklmnop", "STRIPE="},
	{"stripe restricted key", "key rk_live_51Habcdefghijklmnop set", "rk_live_51Habcdefghijklmnop", "set"},
	{"stripe publishable key", "pk_live_51Habcdefghijklmnop", "pk_live_51Habcdefghijklmnop", ""},
	{"stripe test key", "sk_test_51Habcdefghijklmnop", "sk_test_51Habcdefghijklmnop", ""},
	{"gcp service account private key", `{"type": "service_account", "private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n", "client_email": "svc@project.iam.gserviceaccount.com"}`, "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "client_email"},
	{"gcp service account private key id", `{"private_key_id": "0123456789abcdef0123456789abcdef01234567"}`, "0123456789abcdef0123456789abcdef01234567", "private_key_id"},
	{"gcp api key", "key=AIzaSyA1234567890abcdefghijklmnopqrstuv ok", "AIzaSyA1234567890abcdefghijklmnopqrstuv", "ok"},
	{"azure storage connection string", "DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=Zm9vYmFyYmF6cXV4+/abc==;EndpointSuffix=core.windows.net", "Zm9vYmFyYmF6cXV4+/abc==", "EndpointSuffix=core.windows.net"},
	{"azure service bus connection string", "Endpoint=sb://ns.servicebus.windows.net/;SharedAccessKeyName=root;SharedAccessKey=Zm9vYmFyYmF6cXV4YWJj=", "Zm9vYmFyYmF6cXV4YWJj=", "SharedAccessKeyName=root"},
	{"azure sas signature", "https://acct.blob.core.windows.net/c/b?sv=2022-11-02&sp=r&sig=abcDEF123%2Bxyz%3D&se=2026", "abcDEF123%2Bxyz%3D", "se=2026"},
}

func redactUnfiltered(r Redactor, s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, Redacted)
	}
	for _, p := range secretPatterns {
		s = p.pattern.ReplaceAllString(s, p.replace)
	}
	return s
}

func TestRedactorMasksEveryCredentialShape(t *testing.T) {
	r := NewRedactor()
	for _, c := range redactionCases {
		t.Run(c.name, func(t *testing.T) {
			got := r.String(c.input)
			if strings.Contains(got, c.secret) || !strings.Contains(got, Redacted) || !strings.Contains(got, c.keep) {
				t.Fatalf("String(%q) = %q; must drop %q and keep %q", c.input, got, c.secret, c.keep)
			}
		})
	}
}

func TestRedactorLiteralFiltersSkipNoMatch(t *testing.T) {
	r := NewRedactor("exact-secret-value")
	inputs := []string{benchmarkToolOutput(MaxTextBytes), "exact-secret-value and KEY: value", "Ünïcödé tëxt with ſecret=abcdefgh123 and \u212aelvin",
		"apikey: abcdefgh123", "ACCESS-KEY=abcdefgh123", "private_key = 'abcdefgh123'", "api__key=abcdefgh123", "monkey=abcdefgh123", "oauth = abcdefgh123", "author = someone"}
	for _, c := range redactionCases {
		inputs = append(inputs, c.input)
	}
	for _, in := range inputs {
		if got, want := r.String(in), redactUnfiltered(r, in); got != want {
			t.Fatalf("filtered redaction differs for %q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRedactorLeavesOrdinaryTextAlone(t *testing.T) {
	text := benchmarkToolOutput(MaxTextBytes)
	if got := NewRedactor().String(text); got != text {
		t.Fatalf("ordinary tool output changed")
	}
}

func TestSealNeverStoresPartOfASecretThatStraddlesTheLimit(t *testing.T) {
	secrets := []struct {
		value           string
		spansWhitespace bool
	}{
		{"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", false},
		{"0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", false},
		{"https://user:QWERTYUIOPASDFGHJKLZ@forge.example", false},
		{"exactRunSecretValue0123", false},
		{"Bearer QWERTYUIOPASDFGHJKLZ", true},
		{"password: QWERTYUIOPASDFGHJKLZ", true},
		{"-----BEGIN PRIVATE KEY-----\nQWERTYUIOPASDFGHJKLZ\nQWERTYUIOPASDFGHJKLZ\n-----END PRIVATE KEY-----", true},
	}
	r := NewRedactor("exactRunSecretValue0123")
	filler := strings.Repeat("ordinary words ", 400)
	shrinking := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("QUFBQUFBQUFBQUFBQUFB\n", 60) + "-----END PRIVATE KEY-----\n"
	for _, secret := range secrets {
		for offset := MaxTextBytes - 120; offset < MaxTextBytes+redactionOverlap+40; offset += 7 {
			text := shrinking + filler[:offset-len(shrinking)] + " " + secret.value + " " + filler
			got := r.Seal(Entry{Kind: KindToolOutput, Text: text})
			if len(got.Text) > MaxTextBytes || !got.Clipped {
				t.Fatalf("offset %d: %d bytes, clipped=%v", offset, len(got.Text), got.Clipped)
			}
			for _, fragment := range []string{"QWERTYUI", "ABCDEFGH", "0123456789abcdef", "dozjgNry", "eyJzdWIi", "exactRun"} {
				if strings.Contains(got.Text, fragment) {
					t.Fatalf("offset %d: %q stored a fragment %q of %q", offset, got.Text[len(got.Text)-80:], fragment, secret.value)
				}
			}
			if secret.spansWhitespace {
				continue
			}
			reference := Clamp(Entry{Kind: KindToolOutput, Text: r.String(text)})
			completeTokens := got.Text[:strings.LastIndexAny(got.Text, regexpSpace)+1]
			if !strings.HasPrefix(reference.Text, completeTokens) {
				t.Fatalf("offset %d: sealed text before its last token is not a prefix of redact-then-clamp:\n%q\n%q", offset, got.Text[len(got.Text)-60:], reference.Text[len(got.Text)-60:min(len(reference.Text), len(got.Text)+20)])
			}
		}
	}
}

func TestSealRedactsWholeFieldWithoutWhitespaceNearTheLimit(t *testing.T) {
	r := NewRedactor()
	blob := strings.Repeat("x", MaxTextBytes-10) + ",ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" + strings.Repeat("y", 2*redactionOverlap)
	got := r.Seal(Entry{Kind: KindToolOutput, Text: blob})
	want := Clamp(Entry{Kind: KindToolOutput, Text: r.String(blob)})
	if got.Text != want.Text || strings.Contains(got.Text, "ghp_") || !got.Clipped {
		t.Fatalf("a whitespace-free field must be redacted whole before it is clipped: %q", got.Text[len(got.Text)-40:])
	}
}

func TestSealRedactsWholeFieldWhenAnExactSecretHoldsWhitespace(t *testing.T) {
	secret := "two word secret phrase"
	r := NewRedactor(secret)
	shrinking := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("QUFBQUFBQUFBQUFBQUFB\n", 60) + "-----END PRIVATE KEY-----\n"
	text := shrinking + strings.Repeat("a", MaxTextBytes+redactionOverlap-len(shrinking)-10) + " " + secret + strings.Repeat(" b", redactionOverlap)
	got := r.Seal(Entry{Kind: KindToolOutput, Text: text})
	if strings.Contains(got.Text, "two") || !got.Clipped {
		t.Fatalf("partial exact secret stored: %q", got.Text[len(got.Text)-40:])
	}
}

func TestSealMatchesRedactThenClampForOrdinaryEntries(t *testing.T) {
	r := NewRedactor("0123456789abcdef0123456789abcdef0123456789abcdef")
	long := benchmarkToolOutput(10 << 10)
	paths := make([]string, MaxPaths+4)
	for i := range paths {
		paths[i] = "pkg/" + strings.Repeat("d/", 200) + "password=hunter2hunter2"
	}
	plan := make([]Step, MaxPlanSteps+3)
	for i := range plan {
		plan[i] = Step{Content: "check token=abcdefgh12345678 " + strings.Repeat("step ", 80), Status: "in_progress"}
	}
	e := Entry{Kind: KindToolCall, ToolCallID: "t1", ToolKind: "execute", Status: "completed", Title: "run " + long[:600], Input: long[:2000], Text: long, Paths: paths, Plan: plan}
	got := r.Seal(e)
	reference := Clamp(redactFields(r, e))
	if got.Text != reference.Text || got.Input != reference.Input || got.Title != reference.Title || got.Clipped != reference.Clipped {
		t.Fatalf("sealed text fields differ from redact-then-clamp")
	}
	if len(got.Paths) != MaxPaths || len(got.Plan) != MaxPlanSteps {
		t.Fatalf("paths %d, plan %d", len(got.Paths), len(got.Plan))
	}
	for i := range got.Paths {
		if got.Paths[i] != reference.Paths[i] {
			t.Fatalf("path %d differs: %q vs %q", i, got.Paths[i], reference.Paths[i])
		}
	}
	for i := range got.Plan {
		if got.Plan[i] != reference.Plan[i] {
			t.Fatalf("step %d differs: %q vs %q", i, got.Plan[i], reference.Plan[i])
		}
	}
	if e.Paths[0] != paths[0] || len(e.Paths) != MaxPaths+4 {
		t.Fatalf("Seal changed its argument")
	}
}

func redactFields(r Redactor, e Entry) Entry {
	e.Text, e.Title, e.Input = r.String(e.Text), r.String(e.Title), r.String(e.Input)
	paths := make([]string, len(e.Paths))
	for i, p := range e.Paths {
		paths[i] = r.String(p)
	}
	e.Paths = paths
	steps := make([]Step, len(e.Plan))
	for i, s := range e.Plan {
		steps[i] = Step{Content: r.String(s.Content), Status: s.Status}
	}
	e.Plan = steps
	return e
}
