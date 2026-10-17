package scan

import (
	"slices"
	"testing"
)

func TestArray(t *testing.T) {
	in := "(\n    \"com.adobe.pdf\",\n    \"public.data\"\n)"
	if got := array(in); !slices.Equal(got, []string{"com.adobe.pdf", "public.data"}) {
		t.Errorf("array = %q", got)
	}
	if got := array(nullMarker); got != nil {
		t.Errorf("array(null) = %q", got)
	}
}

func TestUnquote(t *testing.T) {
	cases := map[string]string{
		`"plain"`:               "plain",
		`"say \"hi\""`:          `say "hi"`,
		`"g\U00fczel"`:          "güzel",
		`public.data`:           "public.data",
		`"https://a.com/x?y=1"`: "https://a.com/x?y=1",
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%s) = %q, want %q", in, got, want)
		}
	}
}
