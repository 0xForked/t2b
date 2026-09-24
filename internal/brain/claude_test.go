package brain

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"clean", `{"buy":true,"reasoning":"ok"}`, `{"buy":true,"reasoning":"ok"}`},
		{"markdown fence", "```json\n{\"buy\":true}\n```", `{"buy":true}`},
		{"leading prose", `Sure, here you go: {"buy":false,"reasoning":"risky"}`, `{"buy":false,"reasoning":"risky"}`},
		{"trailing prose", "{\"sell\":true}\nHope that helps!", `{"sell":true}`},
		{"no json", "no JSON here at all", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractJSON(c.in)
			if got != c.want {
				t.Fatalf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
