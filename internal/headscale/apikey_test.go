package headscale

import "testing"

func TestParseAPIKeyOutput(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"hskey-api-json"`, "hskey-api-json"},
		{`{"apiKey":"hskey-api-object"}`, "hskey-api-object"},
		{"hskey-api-plain\n", "hskey-api-plain"},
	}
	for _, test := range tests {
		got, err := parseAPIKeyOutput(test.input)
		if err != nil || got != test.want {
			t.Fatalf("parseAPIKeyOutput(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
}
