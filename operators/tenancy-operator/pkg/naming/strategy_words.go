/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package naming

import (
	"encoding/binary"
)

// StrategyWords is the name of the word-pair strategy.
const StrategyWords = "words"

// wordsAttempts caps retries.
//
// Higher than the base36 cap because collisions here are plausible rather than
// pathological: the space is hundreds of thousands, not quintillions, so a large
// platform will hit taken names occasionally and should just try again.
const wordsAttempts = 12

func init() { Register(&wordsStrategy{}) }

// wordsStrategy mints `adjective-adjective-noun` names, with a short suffix once
// the plain triple is taken.
//
// For platforms where a name is read aloud — in a support call, a ticket, a
// terminal prompt — and neither a UUID nor a base36 blob survives that. It still
// carries no tenant-supplied meaning, so it leaks nothing about who owns the
// workspace, which is the property the display-name strategy gives up.
//
// THREE WORDS, NOT TWO, and the extra word buys two orders of magnitude:
// 64x63x64 = 258,048 names against the 4,096 a single adjective-noun pair gives.
// What that changes is how often anyone SEES a suffix, since a collision here is
// resolved rather than fatal — across a thousand tenants, a pair collides about
// 122 times and a triple about twice. Two words meant one name in eight came out
// as `mellow-orchard-zy5q`, which is the form this strategy exists to avoid.
//
// It does NOT make the space safe for a namespace that cannot retry. Membership
// names are derived from what they grant precisely so a duplicate grant collides
// instead of creating a second object, and no wordlist survives the birthday
// bound at that job: three words has a ~16% chance of an unresolvable collision
// inside a single 300-grant tenant. That is what pkg/membership's UUIDv5 is for,
// and why it does not use this package.
type wordsStrategy struct{}

func (s *wordsStrategy) Name() string { return StrategyWords }

func (s *wordsStrategy) Generate(req Request) (string, error) {
	if req.Attempt >= wordsAttempts {
		return "", ErrExhausted
	}

	buf, err := Entropy(req)
	if err != nil {
		return "", err
	}

	// Disjoint slices of the same 32-byte draw, so one Entropy call covers the
	// whole name and a seeded Request stays a pure function of its seed.
	// 8 bytes per word (0:8, 8:16, 16:24) leaves 24:32 for the suffix.
	first := index(len(adjectives), buf[0:8])
	second := distinct(len(adjectives), first, buf[8:16])

	name := adjectives[first] + "-" + adjectives[second] + "-" + pick(nouns, buf[16:24])

	// The bare triple is only tried once. After a collision the space is clearly
	// contended, and drawing another bare triple would most likely collide again;
	// a suffix moves the retry into a space that is not contended at all.
	if req.Attempt > 0 {
		name += "-" + suffix(buf[24:], 4)
	}
	return name, nil
}

// index reduces eight bytes of the draw to an offset into a list of size n.
func index(n int, b []byte) int {
	return int(binary.BigEndian.Uint64(b) % uint64(n)) //nolint:gosec // n is a small list length
}

// distinct picks a SECOND offset that is never equal to the first.
//
// Both adjectives come from one list, so an unconstrained draw produces
// `golden-golden-dune` roughly one name in sixty-four — which reads as a bug
// rather than as a name, and is the kind of thing a tenant screenshots.
//
// Chosen from the n-1 values that are not `avoid`, then mapped back, so every
// allowed pair stays equally likely. The cost is 64x63 rather than 64x64: 4,096
// names given up out of 262,144, for never rendering a stutter.
func distinct(n, avoid int, b []byte) int {
	if n < 2 {
		return avoid
	}
	i := int(binary.BigEndian.Uint64(b) % uint64(n-1)) //nolint:gosec // n is a small list length
	if i >= avoid {
		i++
	}
	return i
}

// pick chooses one element from eight bytes of the draw.
func pick(from []string, b []byte) string {
	return from[index(len(from), b)]
}

// suffix renders n lowercase base36 characters from the draw.
func suffix(b []byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = base36Digits[int(b[i])%len(base36Digits)]
	}
	return string(out)
}

// adjectives and nouns are deliberately bland: no proper nouns, no words that
// read as a judgement about the tenant, nothing that becomes unfortunate next to
// a customer's own name in a path. Every entry is lowercase ASCII with no
// hyphens, so any pair is a valid DNS-1123 label without further escaping.
var adjectives = []string{
	"amber", "ancient", "autumn", "azure", "bold", "brave", "brisk", "calm",
	"clever", "cobalt", "coral", "crimson", "crisp", "curious", "dawn", "deep",
	"eager", "early", "electric", "emerald", "fair", "fleet", "frosty", "gentle",
	"gilded", "golden", "green", "hidden", "humble", "indigo", "ivory", "jade",
	"keen", "kind", "lively", "lucid", "lunar", "mellow", "merry", "misty",
	"noble", "nimble", "opal", "patient", "polar", "prime", "quiet", "rapid",
	"ruby", "rustic", "sage", "scarlet", "serene", "silent", "silver", "solar",
	"steady", "stellar", "still", "swift", "tidy", "tranquil", "vivid", "wild",
}

var nouns = []string{
	"anchor", "arbor", "aurora", "basin", "beacon", "bridge", "brook", "canyon",
	"cedar", "cirrus", "cliff", "comet", "compass", "coast", "cove", "crest",
	"delta", "dune", "ember", "estuary", "fjord", "forest", "garden", "glacier",
	"grove", "harbor", "haven", "heath", "hollow", "horizon", "island", "juniper",
	"lagoon", "lantern", "ledge", "meadow", "mesa", "meridian", "moor", "nebula",
	"oasis", "orchard", "outpost", "pillar", "plateau", "prairie", "quarry", "reef",
	"ridge", "river", "sable", "savanna", "shore", "signal", "spire", "spring",
	"summit", "terrace", "thicket", "tundra", "valley", "vista", "willow", "zenith",
}
