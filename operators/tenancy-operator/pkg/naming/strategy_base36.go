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

// StrategyBase36 is the name of the base36 strategy.
const StrategyBase36 = "base36"

// base36Attempts caps retries for the random strategies.
//
// Small on purpose. With this much entropy a collision means something is wrong
// — a seeded PRNG, a cloned process — and grinding through hundreds of attempts
// would turn that into a slow create rather than a visible error.
const base36Attempts = 8

// base36Digits is the alphabet, low to high.
const base36Digits = "0123456789abcdefghijklmnopqrstuvwxyz"

func init() { Register(&base36Strategy{}) }

// base36Strategy mints short lowercase alphanumeric names, the shape kcp uses
// for its own logical cluster identifiers.
//
// Chosen when UUIDs are too long to live in a path but names still must not
// carry meaning. 64 bits rendered in base36 is 13 characters — a quarter of a
// UUID, still unguessable, and still nothing a tenant can read intent into.
type base36Strategy struct{}

func (s *base36Strategy) Name() string { return StrategyBase36 }

func (s *base36Strategy) Generate(req Request) (string, error) {
	if req.Attempt >= base36Attempts {
		return "", ErrExhausted
	}

	buf, err := Entropy(req)
	if err != nil {
		return "", err
	}

	// A leading digit is legal in a DNS-1123 label, but these names get pasted
	// into paths, kubeconfigs and shell variables, where a leading digit is a
	// recurring nuisance. Forcing a letter costs ~2.6 bits and avoids all of it.
	n := binary.BigEndian.Uint64(buf[:8])
	return string(base36Digits[10+n%26]) + encodeBase36(n), nil
}

// encodeBase36 renders n in base36, lowest digit last.
func encodeBase36(n uint64) string {
	if n == 0 {
		return "0"
	}
	// 64 bits needs at most 13 base36 digits.
	var out [13]byte
	i := len(out)
	for n > 0 {
		i--
		out[i] = base36Digits[n%36]
		n /= 36
	}
	return string(out[i:])
}
