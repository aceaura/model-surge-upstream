package resolve

import (
	"encoding/json"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

func merged(t *testing.T, defaults, client, overrides string) map[string]any {
	t.Helper()
	raw, err := MergeParams(json.RawMessage(defaults), json.RawMessage(client), json.RawMessage(overrides))
	if err != nil {
		t.Fatalf("MergeParams: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMergeDefaultsOnlyFillGaps(t *testing.T) {
	got := merged(t, `{"temperature":0.6,"top_p":0.9}`, `{"temperature":0.1}`, `{}`)
	if got["temperature"] != 0.1 {
		t.Errorf("client should win over defaults, got %v", got["temperature"])
	}
	if got["top_p"] != 0.9 {
		t.Errorf("defaults should fill absent keys, got %v", got["top_p"])
	}
}

func TestMergeOverridesWinAll(t *testing.T) {
	got := merged(t, `{"max_tokens":1024}`, `{"max_tokens":2048}`, `{"max_tokens":8192}`)
	if got["max_tokens"] != float64(8192) {
		t.Errorf("overrides must have the highest priority, got %v", got["max_tokens"])
	}
}

func TestMergeRecursesIntoObjects(t *testing.T) {
	got := merged(t,
		`{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{"thinking":{"budget_tokens":4096}}`,
		`{"thinking":{"budget_tokens":65535}}`)
	thinking, ok := got["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %T", got["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("nested key from defaults should survive, got %v", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(65535) {
		t.Errorf("nested override should win, got %v", thinking["budget_tokens"])
	}
}

func TestMergeReplacesArraysWholesale(t *testing.T) {
	got := merged(t, `{"stop":["a","b","c"]}`, `{"stop":["z"]}`, `{}`)
	stop, ok := got["stop"].([]any)
	if !ok {
		t.Fatalf("stop = %T", got["stop"])
	}
	if len(stop) != 1 || stop[0] != "z" {
		t.Errorf("arrays replace wholesale, got %v", stop)
	}
}

func TestMergeReplacesWhenTypesDiffer(t *testing.T) {
	got := merged(t, `{"thinking":{"budget_tokens":1}}`, `{"thinking":false}`, `{}`)
	if got["thinking"] != false {
		t.Errorf("non-object should replace an object, got %v", got["thinking"])
	}
}

func TestMergeNullOverwrites(t *testing.T) {
	got := merged(t, `{"temperature":0.6}`, `{"temperature":null}`, `{}`)
	v, present := got["temperature"]
	if !present {
		t.Fatal("explicit null should stay present as a key")
	}
	if v != nil {
		t.Errorf("temperature = %v, want nil", v)
	}
}

func TestMergeEmptyLayers(t *testing.T) {
	for _, tc := range [][3]string{
		{``, ``, ``},
		{`{}`, `{}`, `{}`},
		{`null`, `null`, `null`},
	} {
		raw, err := MergeParams(json.RawMessage(tc[0]), json.RawMessage(tc[1]), json.RawMessage(tc[2]))
		if err != nil {
			t.Fatalf("MergeParams(%v): %v", tc, err)
		}
		if string(raw) != `{}` {
			t.Errorf("MergeParams(%v) = %s, want {}", tc, raw)
		}
	}
}

func TestMergeDoesNotMutateInputs(t *testing.T) {
	defaults := json.RawMessage(`{"thinking":{"type":"enabled"}}`)
	before := string(defaults)
	if _, err := MergeParams(defaults, json.RawMessage(`{"thinking":{"budget_tokens":1}}`), nil); err != nil {
		t.Fatal(err)
	}
	if string(defaults) != before {
		t.Errorf("defaults mutated: %s", defaults)
	}
}

func TestMergeRejectsNonObjects(t *testing.T) {
	cases := [][3]string{
		{`[1]`, `{}`, `{}`},
		{`{}`, `"str"`, `{}`},
		{`{}`, `{}`, `7`},
		{`{oops}`, `{}`, `{}`},
	}
	for _, tc := range cases {
		_, err := MergeParams(json.RawMessage(tc[0]), json.RawMessage(tc[1]), json.RawMessage(tc[2]))
		if err == nil {
			t.Errorf("MergeParams(%v) should fail", tc)
			continue
		}
		if !apperr.Is(err, apperr.InvalidJSON) {
			t.Errorf("MergeParams(%v) code = %q", tc, apperr.CodeOf(err))
		}
	}
}
