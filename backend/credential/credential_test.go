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

func TestBailianCredentialPersistenceAndRedaction(t *testing.T) {
	c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-inference", BailianAccessKeyID: "LTAI-very-secret-id", BailianAccessKeySecret: "very-secret-access-key", ConsoleAccessToken: "console-secret-token", ConsoleVerifiedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	raw, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(raw)
	if err != nil || back != c {
		t.Fatalf("credential JSON persistence: %v", err)
	}
	v := c.Redact()
	if v.BailianAccessKeyID != "***" || v.BailianAccessKeySecret != "***" || v.ConsoleAccessToken != "***" || !v.ConsoleVerifiedAt.Equal(c.ConsoleVerifiedAt) {
		t.Fatal("AK/SK must be purely masked while exposing verified timestamp")
	}
	view, _ := json.Marshal(v)
	for _, secret := range []string{c.BailianAccessKeyID, c.BailianAccessKeySecret, c.ConsoleAccessToken} {
		if strings.Contains(string(view), secret) || strings.Contains(c.String(), secret) {
			t.Fatal("Bailian secret leaked")
		}
	}
	for _, id := range []string{"", "   ", "id"} {
		for _, secret := range []string{"", "   ", "secret"} {
			c.BailianAccessKeyID, c.BailianAccessKeySecret = id, secret
			wantError := (strings.TrimSpace(id) == "") != (strings.TrimSpace(secret) == "")
			if (c.Validate() != nil) != wantError {
				t.Fatal("AK/SK validation must require a complete pair")
			}
		}
	}
	c.ConsoleVerifiedAt = time.Time{}
	raw, _ = c.Encode()
	view, _ = json.Marshal(c.Redact())
	if strings.Contains(string(raw), "console_verified_at") || strings.Contains(string(view), "console_verified_at") {
		t.Fatal("zero verified timestamp must be omitted")
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]struct{ raw, wants string }{
		"unknown kind":          {`{"kind":"static_token","token":"x"}`, "unsupported credential kind"},
		"missing kind":          {`{"api_key":"sk-abcdefghijkl"}`, "kind is required"},
		"empty key":             {`{"kind":"api_key","api_key":""}`, "api_key is required"},
		"blank key":             {`{"kind":"api_key","api_key":"   "}`, "api_key is required"},
		"absent key":            {`{"kind":"api_key"}`, "api_key is required"},
		"not json":              {`not json`, "not valid json"},
		"kiro deferred":         {`{"kind":"kiro_desktop"}`, "unsupported credential kind"},
		"oauth missing refresh": {`{"kind":"oauth_refresh","account_id":"acc-1"}`, "refresh_token is required"},
		"oauth missing account": {`{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl"}`, "account_id is required"},
		"oauth blank account":   {`{"kind":"oauth_refresh","refresh_token":"rt-abcdefghijkl","account_id":"  "}`, "account_id is required"},
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
	spec, _ := provider.Get("kimi.global.subscribe.coding")
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

func TestDecodeKiro(t *testing.T) {
	raw := []byte(`{"kind":"kiro_refresh","refresh_token":"rt-secret","profile_arn":"arn:aws:codewhisperer:eu-central-1:123:profile/test","region":"us-east-1","api_region":"eu-west-1","client_id":"client","client_secret":"client-secret","access_token":"access-secret"}`)
	c, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := provider.Get("kiro.global.subscribe.standard")
	if err := c.ValidateAgainstProvider(spec); err != nil {
		t.Fatal(err)
	}
	if c.KiroAuthRegion() != "us-east-1" || c.KiroAPIRegion() != "eu-west-1" {
		t.Fatal("authentication and API regions must remain separate")
	}
	encoded, _ := c.Encode()
	back, err := Decode(encoded)
	if err != nil || back != c {
		t.Fatal("kiro credential round trip failed")
	}
	v := c.Redact()
	if v.RefreshToken != "***" || v.ClientSecret != "***" || v.ProfileARN != c.ProfileARN {
		t.Fatal("kiro redaction lost metadata or exposed secret fragments")
	}
	view, _ := json.Marshal(v)
	for _, secret := range []string{c.RefreshToken, c.ClientSecret, c.AccessToken} {
		if strings.Contains(string(view), secret) || strings.Contains(c.String(), secret) {
			t.Fatal("kiro credential leaked a secret")
		}
	}
	c.APIRegion = ""
	if c.KiroAPIRegion() != "eu-central-1" {
		t.Fatal("profile ARN region must determine API region")
	}
}

func TestDecodeKiroRejects(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"kiro_refresh"}`,
		`{"kind":"kiro_refresh","refresh_token":"rt","client_id":"id"}`,
		`{"kind":"kiro_refresh","refresh_token":"rt","client_secret":"secret"}`,
		`{"kind":"kiro_refresh","refresh_token":"rt","region":"us-east-1.attacker.test"}`,
		`{"kind":"kiro_refresh","refresh_token":"rt","api_region":"../secret"}`,
		`{"kind":"kiro_refresh","refresh_token":"rt","profile_arn":"bad-profile"}`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("accepted invalid credential: %s", raw)
		}
	}
	minimal, err := Decode([]byte(`{"kind":"kiro_refresh","refresh_token":"rt"}`))
	if err != nil || minimal.KiroAuthRegion() != "us-east-1" {
		t.Fatal("Desktop refresh-only credential must be accepted")
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

func TestConsoleAccessTokenRoundTripAndRedact(t *testing.T) {
	for _, token := range []string{"", "short", "HEAD.console-sensitive-token.TAIL"} {
		t.Run(token, func(t *testing.T) {
			c := Credential{Kind: provider.CredAPIKey, APIKey: "sk-abcdefghijkl", ConsoleAccessToken: token}
			raw, err := c.Encode()
			if err != nil {
				t.Fatal(err)
			}
			back, err := Decode(raw)
			if err != nil || back != c {
				t.Fatalf("credential JSON round trip failed: %v", err)
			}
			want := "***"
			if token == "" {
				want = ""
			}
			if got := c.Redact().ConsoleAccessToken; got != want {
				t.Errorf("console token must be fully masked, got %q", got)
			}
			redacted, err := json.Marshal(c.Redact())
			if err != nil {
				t.Fatal(err)
			}
			var view map[string]string
			if err := json.Unmarshal(redacted, &view); err != nil {
				t.Fatal(err)
			}
			if view["console_access_token"] != want {
				t.Error("serialized console token must be fully masked")
			}
			if token != "" {
				for _, fragment := range []string{token, token[:min(4, len(token))], token[max(0, len(token)-4):]} {
					if strings.Contains(c.String(), fragment) || strings.Contains(string(redacted), fragment) {
						t.Error("console token or its prefix/suffix leaked")
					}
				}
				if !strings.Contains(c.String(), "ConsoleAccessToken:***") {
					t.Error("String must fully mask the console token")
				}
			} else if _, exists := view["console_access_token"]; exists {
				t.Error("empty optional console token should be omitted")
			}
		})
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
