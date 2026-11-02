package peer

import (
	"strings"
	"testing"
)

func TestHopToken(t *testing.T) {
	key := NewHopKey()
	if len(key) != hopKeyBytes {
		t.Fatalf("NewHopKey returned %d bytes", len(key))
	}
	token := HopToken(key, "uds:/tmp/cc-socks/1.sock")
	if len(token) != 24 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("HopToken = %q, want 24 hex digits", token)
	}
	if again := HopToken(key, "uds:/tmp/cc-socks/1.sock"); again != token {
		t.Fatalf("HopToken is not stable: %q vs %q", again, token)
	}
	if other := HopToken(key, "uds:/tmp/cc-socks/2.sock"); other == token {
		t.Fatal("two addresses share a hop token")
	}
	otherKey := NewHopKey()
	if other := HopToken(otherKey, "uds:/tmp/cc-socks/1.sock"); other == token {
		t.Fatal("two keys share a hop token")
	}
}

func TestExtendChain(t *testing.T) {
	chain := ExtendChain(nil, "aaaaaaaaaaaaaaaaaaaaaaaa")
	if len(chain) != 1 || chain[0] != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("ExtendChain = %#v", chain)
	}

	long := make([]string, 32)
	for i := range long {
		long[i] = strings.Repeat(string(rune('a'+i%6)), 24)
	}
	extended := ExtendChain(long, "bbbbbbbbbbbbbbbbbbbbbbbb")
	if len(extended) != maxHopChain {
		t.Fatalf("chain length = %d, want %d", len(extended), maxHopChain)
	}
	if extended[len(extended)-1] != "bbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("our token is not last: %#v", extended)
	}
	if extended[0] != long[1] {
		t.Fatalf("the chain did not slide by one: %#v", extended[0])
	}
}

func TestCheckChain(t *testing.T) {
	limits := DefaultLimits
	if reason := CheckChain(nil, nil, limits); reason != "" {
		t.Fatalf("an empty chain was refused: %q", reason)
	}

	runaway := make([]string, limits.MaxChainLength+1)
	for i := range runaway {
		runaway[i] = strings.Repeat("a", 24)
	}
	if reason := CheckChain(runaway, nil, limits); reason != ReasonHopRunaway {
		t.Fatalf("a 29-link chain = %q, want %q", reason, ReasonHopRunaway)
	}
	if reason := CheckChain(runaway[:limits.MaxChainLength], nil, limits); reason != "" {
		t.Fatalf("a 28-link chain was refused: %q", reason)
	}

	own := map[string]bool{}
	looping := make([]string, 0, limits.MaxSelfHops)
	for i := 0; i < limits.MaxSelfHops; i++ {
		token := "self" + string(rune('a'+i)) + strings.Repeat("f", 19)
		own[token] = true
		looping = append(looping, token)
	}
	if reason := CheckChain(looping, own, limits); reason != ReasonHopLoop {
		t.Fatalf("ten self hops = %q, want %q", reason, ReasonHopLoop)
	}
	if reason := CheckChain(looping[:limits.MaxSelfHops-1], own, limits); reason != "" {
		t.Fatalf("nine self hops were refused: %q", reason)
	}
}
