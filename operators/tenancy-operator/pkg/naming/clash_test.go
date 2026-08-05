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

package naming_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/pkg/naming"
)

// store stands in for the API server: a set of names, each owned by somebody.
//
// The interesting behaviour of this package is entirely about what happens when
// two callers want the same name, and that cannot be exercised against a
// strategy in isolation — Apply and Seeded are where collisions are resolved.
type store struct {
	mu    sync.Mutex
	owner map[string]string
}

func newStore() *store { return &store{owner: map[string]string{}} }

var errTaken = errors.New("already exists")

// create is the Apply-shaped half: any existing name is a conflict.
func (s *store) create(name, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.owner[name]; exists {
		return errTaken
	}
	s.owner[name] = by
	return nil
}

// claim is the Seeded-shaped half: an existing name is ours to adopt when we put
// it there, and somebody else's otherwise.
func (s *store) claim(by string) func(string) (bool, error) {
	return func(name string) (bool, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch existing, ok := s.owner[name]; {
		case !ok:
			s.owner[name] = by
			return true, nil
		case existing == by:
			return true, nil
		default:
			return false, nil
		}
	}
}

func (s *store) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.owner)
}

// Every name Apply hands back must be distinct and actually created, however
// contended the strategy's space is. `words` is the case that matters: 258,048
// bare triples is the smallest space here by orders of magnitude, so collisions
// are reachable rather than theoretical, and a strategy that silently returned a
// taken name would hand two tenants one workspace.
func TestApplyNeverReturnsADuplicate(t *testing.T) {
	const want = 500

	for _, strategyName := range naming.Registered() {
		if strategyName == naming.StrategyDisplayName {
			// Derived from the display name, so 500 identical requests exhaust its
			// candidates by design — covered separately below.
			continue
		}

		t.Run(strategyName, func(t *testing.T) {
			s, err := naming.Get(strategyName)
			require.NoError(t, err)

			st := newStore()
			seen := map[string]bool{}

			for i := 0; i < want; i++ {
				caller := fmt.Sprintf("caller-%d", i)
				name, err := naming.Apply(s, naming.Request{Kind: naming.KindProject, DisplayName: "Shared"},
					func(candidate string) error { return st.create(candidate, caller) },
					func(e error) bool { return errors.Is(e, errTaken) },
				)
				require.NoError(t, err, "Apply gave up after %d names", len(seen))

				assert.False(t, seen[name], "Apply returned %q twice", name)
				seen[name] = true
				assert.NoError(t, naming.Validate(name))
			}

			assert.Equal(t, want, st.len(), "every name must correspond to one created object")
		})
	}
}

// A strategy whose candidates run out must say so rather than spin. `displayname`
// is deterministic on attempt 0 and suffixes afterwards, so the only way to
// exhaust it is to take every candidate — which is what a saturating store does.
func TestApplyGivesUpRatherThanSpinning(t *testing.T) {
	for _, strategyName := range naming.Registered() {
		t.Run(strategyName, func(t *testing.T) {
			s, err := naming.Get(strategyName)
			require.NoError(t, err)

			attempts := 0
			_, err = naming.Apply(s, naming.Request{Kind: naming.KindProject, DisplayName: "Acme"},
				func(string) error { attempts++; return errTaken },
				func(e error) bool { return errors.Is(e, errTaken) },
			)

			require.ErrorIs(t, err, naming.ErrExhausted)
			assert.Less(t, attempts, 100, "%s tried %d times before giving up", strategyName, attempts)
		})
	}
}

// The property the whole seeded path exists for: a fleet of distinct identities
// each ends up with exactly ONE object, nobody adopts anybody else's, and a
// replay — a requeue, a restart — returns the same name rather than seeding a
// second object.
//
// Run against `words` specifically because its space is small enough that two
// seeds really do derive the same candidate — 2000 users into 258,048 names
// collides with near-certainty. With uuid the test would pass without ever
// exercising the ownership check.
func TestSeededFleetConvergesWithoutCrossAdoption(t *testing.T) {
	const users = 2000

	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	st := newStore()
	assigned := map[string]string{} // user -> name

	for i := 0; i < users; i++ {
		user := fmt.Sprintf("user-%d", i)
		name, err := naming.Seeded(s, naming.Request{Kind: naming.KindTenant, Seed: user}, st.claim(user))
		require.NoError(t, err, "seeding %s", user)
		assigned[user] = name
	}

	// One object per user, and no two users on the same object.
	assert.Equal(t, users, st.len())
	names := map[string]string{}
	for user, name := range assigned {
		if other, clash := names[name]; clash {
			t.Fatalf("%s and %s were both seeded onto %q", user, other, name)
		}
		names[name] = user
		assert.Equal(t, user, st.owner[name], "%s does not own the object it was assigned", user)
	}

	// Replay: every user reconciles again against the populated store and must
	// land on the object they already have.
	for user, want := range assigned {
		got, err := naming.Seeded(s, naming.Request{Kind: naming.KindTenant, Seed: user}, st.claim(user))
		require.NoError(t, err)
		assert.Equal(t, want, got, "replay moved %s to a different object", user)
	}
	assert.Equal(t, users, st.len(), "replay created objects")
}

// The same identity seeding two KINDS must land on two objects. They live in
// different workspaces in production, so a collision here would not even be
// caught by a uniqueness constraint — it would just be wrong.
func TestSeededKindsDoNotCollide(t *testing.T) {
	for _, strategyName := range naming.Registered() {
		if strategyName == naming.StrategyDisplayName {
			// Derives from the display name, which the caller varies per kind.
			continue
		}

		t.Run(strategyName, func(t *testing.T) {
			s, err := naming.Get(strategyName)
			require.NoError(t, err)

			clashes := 0
			for i := 0; i < 500; i++ {
				seed := fmt.Sprintf("user-%d", i)
				tenant, err := s.Generate(naming.Request{Kind: naming.KindTenant, Seed: seed})
				require.NoError(t, err)
				proj, err := s.Generate(naming.Request{Kind: naming.KindProject, Seed: seed})
				require.NoError(t, err)
				if tenant == proj {
					clashes++
				}
			}
			assert.Zero(t, clashes, "%d seeds derived one name for both kinds", clashes)
		})
	}
}

// Golden values. Changing how a seeded name is derived RENAMES every object
// already seeded — and because the name is also the workspace name, that means
// orphaning a workspace path rather than migrating it. This test exists so that
// change cannot happen quietly during a refactor.
//
// If it fails and the new derivation is intended, the values are safe to update
// ONLY on a deployment with no seeded objects yet.
//
// It fired for real on the Organization→Tenant / Account→Project rename: Kind's
// STRING VALUE is part of the digest, so renaming the constant's value re-seeds
// every derivation. Harmless in production, where entropy is random and only
// `Seed` makes it deterministic — but it is exactly the silent rename this test
// exists to catch, and the fixtures below were updated deliberately rather than
// because they were in the way.
func TestSeededNamesAreStable(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		kind     naming.Kind
		attempt  int
		want     string
	}{
		{"base36", naming.KindTenant, 0, "m2vo6mq1dqkmo"},
		{"base36", naming.KindTenant, 1, "x3er1v5irs4iuv"},
		{"base36", naming.KindProject, 0, "a1dqsb1e1kz1ku"},
		{"displayname", naming.KindTenant, 0, "acme-corp"},
		{"displayname", naming.KindTenant, 1, "acme-corp-8k9m"},
		{"uuid", naming.KindTenant, 0, "d3657a3b-0581-5b64-8935-77a9abbf9735"},
		{"uuid", naming.KindProject, 0, "3207269d-bcb5-5e7f-82cd-5dd88080064b"},
		{"words", naming.KindTenant, 0, "ruby-lunar-plateau"},
		{"words", naming.KindTenant, 1, "solar-gentle-fjord-d2vd"},
		{"words", naming.KindProject, 0, "vivid-silver-nebula"},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.strategy, tc.kind, tc.attempt), func(t *testing.T) {
			s, err := naming.Get(tc.strategy)
			require.NoError(t, err)

			got, err := s.Generate(naming.Request{
				Kind: tc.kind, DisplayName: "Acme Corp", Seed: "seed-fixture", Attempt: tc.attempt,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Entropy is the one place determinism is decided, so it is worth pinning
// directly: a strategy that reads crypto/rand instead would pass every
// single-call test and only fail under a retry.
func TestEntropyIsSeededOrRandom(t *testing.T) {
	req := naming.Request{Kind: naming.KindTenant, Seed: "s"}

	a, err := naming.Entropy(req)
	require.NoError(t, err)
	b, err := naming.Entropy(req)
	require.NoError(t, err)
	assert.Equal(t, a, b, "a seeded draw must be reproducible")

	// Attempt and Kind both participate, or a retry would repeat and the two
	// kinds would collide.
	for _, other := range []naming.Request{
		{Kind: naming.KindTenant, Seed: "s", Attempt: 1},
		{Kind: naming.KindProject, Seed: "s"},
		{Kind: naming.KindTenant, Seed: "t"},
	} {
		c, err := naming.Entropy(other)
		require.NoError(t, err)
		assert.NotEqual(t, a, c, "%+v drew the same bytes", other)
	}

	// Unseeded must not be reproducible, or names would be guessable.
	x, err := naming.Entropy(naming.Request{Kind: naming.KindTenant})
	require.NoError(t, err)
	y, err := naming.Entropy(naming.Request{Kind: naming.KindTenant})
	require.NoError(t, err)
	assert.NotEqual(t, x, y)
	assert.Len(t, x, 32)
}

// Adversarial display names must not produce a name the apiserver will reject.
// The slug path is the only one that takes tenant input, so it is the only one
// that can be attacked with it.
func TestNamesSurviveHostileDisplayNames(t *testing.T) {
	hostile := []string{
		strings.Repeat("a", 300),
		strings.Repeat("-", 80),
		strings.Repeat("é", 100),
		"../../etc/passwd",
		"UPPER CASE WITH  SPACES",
		"emoji 🎉 only 🎉 padding",
		"\t\n\r ",
		"a" + strings.Repeat(".b", 60),
		strings.Repeat("acme-", 20),
	}

	for _, strategyName := range naming.Registered() {
		s, err := naming.Get(strategyName)
		require.NoError(t, err)

		for _, display := range hostile {
			for attempt := 0; attempt < 3; attempt++ {
				for _, seed := range []string{"", "fixed-seed"} {
					req := naming.Request{
						Kind: naming.KindTenant, DisplayName: display, Attempt: attempt, Seed: seed,
					}
					name, err := s.Generate(req)
					if errors.Is(err, naming.ErrExhausted) {
						continue
					}
					require.NoError(t, err, "%s on %q", strategyName, display)
					require.NoError(t, naming.Validate(name),
						"%s produced %q from %q", strategyName, name, display)
				}
			}
		}
	}
}

// Validate is the gate every strategy's output passes through, so its boundary
// is worth pinning exactly rather than approximately.
func TestValidateBoundary(t *testing.T) {
	assert.NoError(t, naming.Validate(strings.Repeat("a", naming.MaxNameLength)))
	assert.Error(t, naming.Validate(strings.Repeat("a", naming.MaxNameLength+1)))
}

// A duplicate registration is a build-time mistake — two packages claiming one
// name — and letting the last one win would make the strategy a deployment gets
// depend on import order.
func TestRegisterRejectsDuplicates(t *testing.T) {
	assert.Panics(t, func() { naming.Register(&fixedStrategy{name: naming.StrategyUUID}) })
	assert.Panics(t, func() { naming.Register(&fixedStrategy{name: ""}) })
}

// An external strategy is exactly the code this package cannot review, so a bad
// one must fail loudly at generation rather than as an opaque apiserver
// rejection halfway through a create.
func TestApplyRejectsAnUnusableName(t *testing.T) {
	_, err := naming.Apply(&fixedStrategy{name: "bad", value: "Not A Label"},
		naming.Request{Kind: naming.KindProject},
		func(string) error { return nil },
		func(error) bool { return false },
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unusable name")
}

type fixedStrategy struct {
	name  string
	value string
}

func (s *fixedStrategy) Name() string                            { return s.name }
func (s *fixedStrategy) Generate(naming.Request) (string, error) { return s.value, nil }

// Both adjectives come from one list, so an unconstrained draw would render
// `golden-golden-dune` about one name in sixty-four. That reads as a bug rather
// than as a name, which is the whole reason the second pick is constrained.
func TestWordsNeverStutters(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	for i := range 5000 {
		name, err := s.Generate(naming.Request{Kind: naming.KindTenant, Seed: fmt.Sprintf("seed-%d", i)})
		require.NoError(t, err)

		parts := strings.Split(name, "-")
		require.Len(t, parts, 3, "a bare name is adjective-adjective-noun")
		assert.NotEqual(t, parts[0], parts[1], "%q repeats its adjective", name)
		assert.NoError(t, naming.Validate(name))
	}
}

// The reason for the third word, pinned as a number rather than a claim: at a
// thousand names a two-word space collides ~12% of the time and a three-word one
// well under 1%, which is the difference between a suffix being normal and being
// rare. Asserted loosely — this is a property of the space, not of a draw.
func TestWordsCollisionRateIsLow(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	seen := map[string]bool{}
	collisions := 0
	const draws = 1000

	for i := range draws {
		name, err := s.Generate(naming.Request{Kind: naming.KindTenant, Seed: fmt.Sprintf("tenant-%d", i)})
		require.NoError(t, err)
		if seen[name] {
			collisions++
		}
		seen[name] = true
	}

	assert.Less(t, collisions, draws/100,
		"%d collisions in %d names: the three-word space is not behaving as ~258k", collisions, draws)
}
