package credential

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		"unknown kind":  {`{"kind":"static_token","token":"x"}`, "unsupported credential kind"},
		"missing kind":  {`{"api_key":"sk-abcdefghijkl"}`, "kind is required"},
		"empty key":     {`{"kind":"api_key","api_key":""}`, "api_key is required"},
		"blank key":     {`{"kind":"api_key","api_key":"   "}`, "api_key is required"},
		"absent key":    {`{"kind":"api_key"}`, "api_key is required"},
		"not json":      {`not json`, "not valid json"},
		"kiro deferred": {`{"kind":"kiro_desktop"}`, "unsupported credential kind"},
		"oauth missing refresh":  {`{"kind":"oauth_refresh","account_id":"acc-1"}`, "refresh_token is required"},
		"oauth missing account":  {`{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl"}`, "account_id is required"},
		"oauth blank account":    {`{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl","account_id":"  "}`, "account_id is required"},
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

func TestDecodeOAuthRefresh(t *testing.T) {
	raw := `{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl","account_id":"acc-123",` +
		`"access_token":"at-zzzzyyyyxxxx","expiry":"2026-10-03T20:00:00Z"}`
	c, err := Decode([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Kind != provider.CredOAuthRefresh || c.RefreshToken != "rt-abcdefghijkl" || c.AccountID != "acc-123" {
		t.Errorf("decoded = %+v", c)
	}
	if c.AccessToken != "at-zzzzyyyyxxxx" || c.Expiry.IsZero() {
		t.Errorf("token state = %+v", c)
	}

	// 初始只粘贴 refresh_token+account_id 也合法:access_token 由续期补
	minimal, err := Decode([]byte(`{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl","account_id":"acc-123"}`))
	if err != nil {
		t.Fatalf("minimal paste should decode: %v", err)
	}
	if minimal.AccessToken != "" || !minimal.Expiry.IsZero() {
		t.Errorf("token state should start empty: %+v", minimal)
	}
}

func TestRedactOAuthRefresh(t *testing.T) {
	c := Credential{Kind: provider.CredOAuthRefresh,
		RefreshToken: "rt-abcdefghijkl", AccessToken: "at-zzzzyyyyxxxx", AccountID: "acc-123"}

	redacted, err := json.Marshal(c.Redact())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"rt-abcdefghijkl", "at-zzzzyyyyxxxx"} {
		if strings.Contains(string(redacted), secret) {
			t.Errorf("redacted view leaked %q: %s", secret, redacted)
		}
	}
	if strings.Contains(string(redacted), "access_token") {
		t.Errorf("短效 access_token 不下发: %s", redacted)
	}
	if !strings.Contains(string(redacted), "acc-123") {
		t.Errorf("account_id 是标识不是秘密,应明文下发: %s", redacted)
	}
}

func TestEncodeRoundTripOAuth(t *testing.T) {
	c := Credential{Kind: provider.CredOAuthRefresh,
		RefreshToken: "rt-abcdefghijkl", AccessToken: "at-zzzzyyyyxxxx",
		Expiry: time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC), AccountID: "acc-123"}
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

func TestWebRefreshTokenRoundTripAndRedact(t *testing.T) {
	c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl",
		WebRefreshToken: "eyJhbGciOiJIUzI1NiJ9.web.refresh"}

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

	redacted, err := json.Marshal(c.Redact())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), c.WebRefreshToken) {
		t.Errorf("redacted view leaked web_refresh_token: %s", redacted)
	}
	if !strings.Contains(string(redacted), "web_refresh_token") {
		t.Errorf("redacted view should keep the field shape: %s", redacted)
	}
}

func TestMask(t *testing.T) {	cases := map[string]string{
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
