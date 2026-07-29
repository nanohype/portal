package worker

import (
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// The trailer is the machine-readable half of portal's commit attribution, so the
// cases that matter are the ones that produce a *malformed* trailer rather than
// no trailer — a parser reading `Co-authored-by:  <>` is worse off than one
// reading nothing.
func TestAttributionTrailer(t *testing.T) {
	cases := []struct {
		name  string
		given string
		email string
		nilN  bool
		nilE  bool
		want  string
	}{
		{name: "a resolved identity", given: "Ada Lovelace", email: "ada@example.com",
			want: "Co-authored-by: Ada Lovelace <ada@example.com>"},
		{name: "no name recorded (a row from before the column existed)", nilN: true, email: "ada@example.com"},
		{name: "no email recorded", given: "Ada Lovelace", nilE: true},
		{name: "neither recorded", nilN: true, nilE: true},
		{name: "an empty name string", given: "   ", email: "ada@example.com"},
		{name: "an empty email string", given: "Ada Lovelace", email: "  "},
		// A newline in either half would end the trailer block and let the rest be
		// read as a second trailer.
		{name: "a newline in the name", given: "Ada\nCo-authored-by: Someone Else", email: "ada@example.com"},
		{name: "a carriage return in the email", given: "Ada", email: "ada@example.com\rx"},
		// Angle brackets would close the address early.
		{name: "angle brackets in the name", given: "Ada <ada@evil>", email: "ada@example.com"},
		{name: "a space in the email", given: "Ada", email: "ada@example.com and more"},
		{name: "surrounding whitespace is trimmed, not rejected", given: "  Ada  ", email: "  ada@example.com  ",
			want: "Co-authored-by: Ada <ada@example.com>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n, e *string
			if !tc.nilN {
				n = ptr(tc.given)
			}
			if !tc.nilE {
				e = ptr(tc.email)
			}
			got := attributionTrailer(n, e)
			if got != tc.want {
				t.Errorf("attributionTrailer:\n  want %q\n  got  %q", tc.want, got)
			}
		})
	}
}

func TestWithAttribution(t *testing.T) {
	const msg = "tenant: create analytics on prod-eks\n\nWritten by portal on behalf of 01J... (operation 01K...)."

	t.Run("appends a trailer block separated by a blank line", func(t *testing.T) {
		got := withAttribution(msg, ptr("Ada Lovelace"), ptr("ada@example.com"))
		if !strings.HasSuffix(got, "\n\nCo-authored-by: Ada Lovelace <ada@example.com>\n") {
			t.Errorf("trailer is not a separated block at the end:\n%s", got)
		}
		// `git interpret-trailers` needs exactly one blank line before the block —
		// two would make it prose, none would append it to the previous paragraph.
		if strings.Contains(got, "\n\n\nCo-authored-by:") {
			t.Errorf("more than one blank line before the trailer:\n%s", got)
		}
	})

	t.Run("leaves the message untouched when there is no identity", func(t *testing.T) {
		if got := withAttribution(msg, nil, nil); got != msg {
			t.Errorf("message was modified without an identity:\n  want %q\n  got  %q", msg, got)
		}
	})

	t.Run("does not double the separator on a message that already ends in newlines", func(t *testing.T) {
		got := withAttribution(msg+"\n\n", ptr("Ada"), ptr("ada@example.com"))
		if strings.Contains(got, "\n\n\n") {
			t.Errorf("collapsed trailing newlines incorrectly:\n%q", got)
		}
	})
}
