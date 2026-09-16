package credential

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

func TestDecodeAPIKey(t *testing.T) {
	c, err := Decode([]byte(`{"kind":"api_key","api_key":"sk-abcdefghijkl"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Kind != provider.CredAPIKey || c.APIKey != "sk-abcdefghijkl" {
		t.Errorf("decoded = %+v", c)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]struct{ raw, wants string }{
		"unknown kind":  {`{"kind":"oauth_refresh","refresh_token":"x"}`, "api_key"},
		"missing kind":  {`{"api_key":"sk-abcdefghijkl"}`, "kind is required"},
		"empty key":     {`{"kind":"api_key","api_key":""}`, "api_key is required"},
		"blank key":     {`{"kind":"api_key","api_key":"   "}`, "api_key is required"},
		"absent key":    {`{"kind":"api_key"}`, "api_key is required"},
		"not json":      {`not json`, "not valid json"},
		"kiro deferred": {`{"kind":"kiro_desktop"}`, "unsupported credential kind"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should mention %q", err, tc.wants)
			}
		})
	}
}

func TestValidateAgainstProvider(t *testing.T) {
	spec, _ := provider.Get("kimi")
	ok := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"}
	if err := ok.ValidateAgainstProvider(spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bad := Credential{Kind: "oauth_refresh"}
	err := bad.ValidateAgainstProvider(spec)
	if err == nil {
		t.Fatal("mismatched kind should be rejected")
	}
	if !strings.Contains(err.Error(), string(spec.Credential)) {
		t.Errorf("error %q should state the expected kind %q", err, spec.Credential)
	}
	if !strings.Contains(err.Error(), spec.ID) {
		t.Errorf("error %q should name the provider", err)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"sk":              "***",
		"12345678":        "***",
		"123456789":       "1234***6789",
		"sk-abcdefghijkl": "sk-a***ijkl",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactedSerializationDropsSecret(t *testing.T) {
	c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"}

	plain, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "sk-abcdefghijkl") {
		t.Error("delivery path must carry the real key")
	}

	redacted, err := json.Marshal(c.Redact())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), "sk-abcdefghijkl") {
		t.Errorf("redacted view leaked the key: %s", redacted)
	}
	if !strings.Contains(string(redacted), "api_key") {
		t.Errorf("redacted view should keep the field shape: %s", redacted)
	}
}

func TestStringNeverLeaks(t *testing.T) {
	c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"}
	if strings.Contains(c.String(), "sk-abcdefghijkl") {
		t.Errorf("String() leaked the key: %s", c.String())
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl"}
	raw, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back != c {
		t.Errorf("round trip changed the credential: %+v vs %+v", back, c)
	}
}
