package proxy

import (
	"strings"
	"testing"
)

func TestShortenToolNameLeavesShortNamesAlone(t *testing.T) {
	short := "tool_short"
	if got := shortenToolNameWithMap(short); got != short {
		t.Fatalf("expected %q unchanged, got %q", short, got)
	}
}

func TestShortenToolNamePrefersMcpHumanReadable(t *testing.T) {
	name := "mcp__some-server-with-very-long-identifier__do_something_useful_now"
	got := shortenToolNameWithMap(name)
	if len(got) > kiroToolNameMaxLen {
		t.Fatalf("len > %d: %q (%d)", kiroToolNameMaxLen, got, len(got))
	}
	if !strings.HasPrefix(got, "mcp__do_something") {
		t.Fatalf("expected mcp__ short form, got %q", got)
	}
	if RestoreToolName(got) != name {
		t.Fatalf("restore mismatch: %q -> %q", got, RestoreToolName(got))
	}
}

func TestShortenToolNameDeterministicHash(t *testing.T) {
	name := "this_is_a_very_long_tool_name_that_definitely_exceeds_sixty_three_characters_for_sure_xxx"
	a := shortenToolNameWithMap(name)
	b := shortenToolNameWithMap(name)
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if len(a) != kiroToolNameMaxLen {
		t.Fatalf("expected len == %d, got %d (%q)", kiroToolNameMaxLen, len(a), a)
	}
	if RestoreToolName(a) != name {
		t.Fatalf("restore mismatch: %q -> %q", a, RestoreToolName(a))
	}
}

func TestShortenToolNameDistinctsCollidePrefix(t *testing.T) {
	common := strings.Repeat("a", 60)
	n1 := common + "_one_a"
	n2 := common + "_two_b"
	s1 := shortenToolNameWithMap(n1)
	s2 := shortenToolNameWithMap(n2)
	if s1 == s2 {
		t.Fatalf("hash collision in 8-hex prefix: %q", s1)
	}
	if RestoreToolName(s1) != n1 || RestoreToolName(s2) != n2 {
		t.Fatalf("restore failed: %q->%q, %q->%q",
			s1, RestoreToolName(s1), s2, RestoreToolName(s2))
	}
}

func TestRestoreToolNameUnknownPassThrough(t *testing.T) {
	got := RestoreToolName("never_registered_tool_name")
	if got != "never_registered_tool_name" {
		t.Fatalf("expected pass-through, got %q", got)
	}
}
