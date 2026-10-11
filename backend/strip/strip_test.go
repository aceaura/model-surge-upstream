package strip

import (
	"encoding/json"
	"testing"
)

func body(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestApply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		paths []string
		want  string
	}{
		{
			name:  "top level key",
			raw:   `{"store":false,"model":"m"}`,
			paths: []string{"store"},
			want:  `{"model":"m"}`,
		},
		{
			name:  "nested path",
			raw:   `{"metadata":{"user_id":"u","trace":"t"},"model":"m"}`,
			paths: []string{"metadata.user_id"},
			want:  `{"metadata":{"trace":"t"},"model":"m"}`,
		},
		{
			name:  "emptied parent pruned",
			raw:   `{"reasoning":{"effort":"high"},"model":"m"}`,
			paths: []string{"reasoning.effort"},
			want:  `{"model":"m"}`,
		},
		{
			name:  "multi level prune stops at non-empty",
			raw:   `{"a":{"b":{"c":1},"d":2}}`,
			paths: []string{"a.b.c"},
			want:  `{"a":{"d":2}}`,
		},
		{
			name:  "missing path no-op",
			raw:   `{"model":"m"}`,
			paths: []string{"metadata.user_id"},
			want:  `{"model":"m"}`,
		},
		{
			name:  "non-map intermediate no-op",
			raw:   `{"tools":[1,2]}`,
			paths: []string{"tools.0"},
			want:  `{"tools":[1,2]}`,
		},
		{
			name:  "empty paths no-op",
			raw:   `{"store":false}`,
			paths: nil,
			want:  `{"store":false}`,
		},
		{
			name:  "siblings kept",
			raw:   `{"thinking":{"type":"enabled","budget_tokens":2000}}`,
			paths: []string{"thinking.budget_tokens"},
			want:  `{"thinking":{"type":"enabled"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := body(t, tc.raw)
			Apply(m, tc.paths)
			got, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var gotN, wantN any
			if err := json.Unmarshal(got, &gotN); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &wantN); err != nil {
				t.Fatal(err)
			}
			gb, _ := json.Marshal(gotN)
			wb, _ := json.Marshal(wantN)
			if string(gb) != string(wb) {
				t.Fatalf("got %s, want %s", gb, wb)
			}
		})
	}
}

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"store", true},
		{"metadata.user_id", true},
		{"a.b.c", true},
		{"", false},
		{".", false},
		{".a", false},
		{"a.", false},
		{"a..b", false},
	} {
		if got := Valid(tc.path); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
