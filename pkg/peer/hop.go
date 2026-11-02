package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// hopKeyBytes is the size of the per-process hop key. It never leaves the
// process: what travels is the token derived from it, which nobody else can
// forge and which cannot be traced back to our address.
const hopKeyBytes = 32

// hopTokenLength is the length of a hop token in hex digits.
const hopTokenLength = 24

// NewHopKey is a fresh hop key, or nil when the system entropy source fails.
func NewHopKey() []byte {
	key := make([]byte, hopKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}

// HopToken is this process's token for one address: the first 24 hex digits of
// HMAC-SHA256(key, ownAddr). The same key and address always give the same
// token, which is how a chain can be checked for our own hops without the chain
// naming us.
func HopToken(key []byte, ownAddr string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ownAddr))
	return hex.EncodeToString(mac.Sum(nil))[:hopTokenLength]
}

// ExtendChain appends our token to the chain an incoming message carried, and
// keeps only the last maxHopChain links: an old chain is worthless past that
// length anyway, and the header has to stay bounded.
func ExtendChain(inherited []string, own string) []string {
	chain := make([]string, 0, len(inherited)+1)
	chain = append(chain, inherited...)
	if own != "" {
		chain = append(chain, own)
	}
	if len(chain) > maxHopChain {
		chain = chain[len(chain)-maxHopChain:]
	}
	return chain
}

// CheckChain applies the anti-loop rules to an incoming chain and returns the
// drop reason, or "" when the message may proceed.
func CheckChain(chain []string, ownTokens map[string]bool, lim Limits) string {
	if len(chain) > lim.MaxChainLength {
		return ReasonHopRunaway
	}
	selfHops := 0
	for _, link := range chain {
		if ownTokens[link] {
			selfHops++
		}
	}
	if selfHops >= lim.MaxSelfHops {
		return ReasonHopLoop
	}
	return ""
}
