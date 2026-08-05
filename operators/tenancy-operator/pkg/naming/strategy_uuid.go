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

import "github.com/google/uuid"

// StrategyUUID is the name of the UUID strategy.
const StrategyUUID = "uuid"

func init() { Register(&uuidStrategy{}) }

// uuidStrategy mints a random UUIDv4.
//
// The default, and the conservative choice: 122 bits of randomness means a
// collision never happens in practice, so the retry loop above it never runs and
// a create is one round trip. The cost is that every workspace path contains a
// string no human can read back over a call — which is the whole reason the other
// strategies exist.
type uuidStrategy struct{}

func (s *uuidStrategy) Name() string { return StrategyUUID }

func (s *uuidStrategy) Generate(req Request) (string, error) {
	if req.Seed != "" {
		// A v5 UUID over the seed: the same identity always derives the same name,
		// so a retry adopts rather than duplicates. Unbounded attempts, because
		// here a collision is a real (if vanishingly unlikely) event to route
		// around rather than a sign of a broken caller.
		buf, err := Entropy(req)
		if err != nil {
			return "", err
		}
		return uuid.NewSHA1(seedNamespace, buf).String(), nil
	}

	// Capped. A second attempt means the impossible happened — far more likely a
	// bug in the caller's collision test than an actual UUID collision — and
	// looping on it would hide that behind an infinite retry.
	if req.Attempt > 1 {
		return "", ErrExhausted
	}
	return uuid.NewString(), nil
}

// seedNamespace fixes the derived-name space. Arbitrary, and it must never
// change: changing it renames every seeded object, and therefore every seeded
// workspace path.
var seedNamespace = uuid.MustParse("6f1a6b1e-4a1e-4c8e-9a5f-2b7a1d3c5e90")
