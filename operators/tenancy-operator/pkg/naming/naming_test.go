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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/pkg/naming"
)

// Every built-in strategy must produce a name the apiserver and kcp will accept,
// on the first attempt and on a retry. A strategy that only validates when the
// display name is well behaved is a strategy that fails in production.
func TestBuiltinStrategiesProduceValidNames(t *testing.T) {
	displayNames := []string{
		"",
		"Acme",
		"Acme  &  Co.",
		"   leading and trailing   ",
		"ÜBER Größe", // non-ASCII: nothing usable survives slugification
		"-----",      // punctuation only
		"9 lives",    // leading digit
		"a-very-long-display-name-that-comfortably-exceeds-the-limit-we-impose",
	}

	for _, strategyName := range naming.Registered() {
		s, err := naming.Get(strategyName)
		require.NoError(t, err)

		for _, display := range displayNames {
			for attempt := 0; attempt < 3; attempt++ {
				req := naming.Request{Kind: naming.KindProject, DisplayName: display, Attempt: attempt}

				name, err := s.Generate(req)
				if errors.Is(err, naming.ErrExhausted) {
					continue
				}
				require.NoError(t, err, "%s attempt %d for %q", strategyName, attempt, display)
				assert.NoError(t, naming.Validate(name),
					"%s attempt %d for %q produced %q", strategyName, attempt, display, name)
			}
		}
	}
}

// The point of a strategy that can vary: attempt 1 must not repeat attempt 0, or
// Apply's retry loop would grind through the same taken name until it gave up.
func TestRetryProducesADifferentName(t *testing.T) {
	for _, strategyName := range []string{naming.StrategyBase36, naming.StrategyWords, naming.StrategyDisplayName} {
		s, err := naming.Get(strategyName)
		require.NoError(t, err)

		first, err := s.Generate(naming.Request{DisplayName: "shared", Attempt: 0})
		require.NoError(t, err)
		second, err := s.Generate(naming.Request{DisplayName: "shared", Attempt: 1})
		require.NoError(t, err)

		assert.NotEqual(t, first, second, "%s repeated a name on retry", strategyName)
	}
}

// The display-name strategy is the only one whose first attempt is predictable,
// and that predictability is its entire reason to exist.
func TestDisplayNameIsDerivedThenDisambiguated(t *testing.T) {
	s, err := naming.Get(naming.StrategyDisplayName)
	require.NoError(t, err)

	name, err := s.Generate(naming.Request{DisplayName: "Acme & Co.", Attempt: 0})
	require.NoError(t, err)
	assert.Equal(t, "acme-co", name)

	// On collision the slug is kept and a suffix added, so the name a human
	// recognises survives the disambiguation.
	name, err = s.Generate(naming.Request{DisplayName: "Acme & Co.", Attempt: 1})
	require.NoError(t, err)
	assert.Regexp(t, `^acme-co-[0-9a-z]{4}$`, name)
}

// A display name that slugifies to nothing must still yield a usable name: the
// caller asked for a Project, and refusing over an unrepresentable label would
// make the strategy unusable for anyone not writing in ASCII.
func TestDisplayNameFallsBackWhenNothingSurvives(t *testing.T) {
	s, err := naming.Get(naming.StrategyDisplayName)
	require.NoError(t, err)

	name, err := s.Generate(naming.Request{DisplayName: "…!!…", Attempt: 0})
	require.NoError(t, err)
	assert.NoError(t, naming.Validate(name))
}

func TestSlugify(t *testing.T) {
	for input, want := range map[string]string{
		"Acme":         "acme",
		"Acme  &  Co.": "acme-co",
		"  padded  ":   "padded",
		"UPPER":        "upper",
		"trailing-":    "trailing",
		"-leading":     "leading",
		"a/b\\c":       "a-b-c",
		"9 lives":      "9-lives",
		"":             "",
		"...":          "",
		"ünïcødé":      "n-c-d",
	} {
		assert.Equal(t, want, naming.Slugify(input), "input %q", input)
	}
}

// Apply owns collision handling. It must keep trying while the name is taken,
// and must NOT retry an error that means something else — a permission failure
// retried as a collision would burn every candidate and report the wrong cause.
func TestApplyRetriesOnlyOnCollision(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	t.Run("retries until free", func(t *testing.T) {
		var seen []string
		taken := map[string]bool{}

		// The first two candidates are already in use.
		name, err := naming.Apply(s, naming.Request{Kind: naming.KindProject},
			func(candidate string) error {
				seen = append(seen, candidate)
				if len(seen) <= 2 {
					taken[candidate] = true
					return errAlreadyExists
				}
				return nil
			},
			func(e error) bool { return errors.Is(e, errAlreadyExists) },
		)

		require.NoError(t, err)
		assert.Len(t, seen, 3)
		assert.Equal(t, seen[2], name, "Apply must return the name that was actually created")
		assert.False(t, taken[name])
	})

	t.Run("surfaces other errors immediately", func(t *testing.T) {
		boom := errors.New("forbidden")
		calls := 0

		_, err := naming.Apply(s, naming.Request{Kind: naming.KindProject},
			func(string) error { calls++; return boom },
			func(e error) bool { return errors.Is(e, errAlreadyExists) },
		)

		require.ErrorIs(t, err, boom)
		assert.Equal(t, 1, calls, "a non-collision error must not be retried")
	})

	t.Run("gives up once candidates run out", func(t *testing.T) {
		_, err := naming.Apply(s, naming.Request{Kind: naming.KindProject},
			func(string) error { return errAlreadyExists },
			func(e error) bool { return errors.Is(e, errAlreadyExists) },
		)
		require.ErrorIs(t, err, naming.ErrExhausted)
	})
}

var errAlreadyExists = errors.New("already exists")

func TestValidateRejectsUnusableNames(t *testing.T) {
	for _, bad := range []string{
		"",
		"Upper",
		"under_score",
		"-leading",
		"trailing-",
		"way-too-long-" + fmt.Sprintf("%0*d", naming.MaxNameLength, 0),
	} {
		assert.Error(t, naming.Validate(bad), "expected %q to be rejected", bad)
	}
	assert.NoError(t, naming.Validate("acme-co"))
}

func TestGetReportsWhatIsAvailable(t *testing.T) {
	_, err := naming.Get("nope")
	require.Error(t, err)
	// The common failure is a typo or an unimported strategy package, and those
	// are indistinguishable without the list.
	assert.Contains(t, err.Error(), naming.StrategyUUID)
}

// A seeded Request must produce the same name every time, in every process.
// That is the whole property: a reconciler that requeues between creating an
// object and recording a pointer to it has to recompute the SAME name and adopt
// what is already there, rather than seed a second one.
func TestSeededGenerationIsDeterministic(t *testing.T) {
	for _, strategyName := range naming.Registered() {
		s, err := naming.Get(strategyName)
		require.NoError(t, err)

		req := naming.Request{Kind: naming.KindTenant, DisplayName: "Acme", Seed: "user-digest"}

		first, err := s.Generate(req)
		require.NoError(t, err)
		for i := 0; i < 20; i++ {
			again, err := s.Generate(req)
			require.NoError(t, err)
			assert.Equal(t, first, again, "%s is not deterministic under a seed", strategyName)
		}

		// Different seed, different name — or every user would seed onto one object.
		other, err := s.Generate(naming.Request{Kind: naming.KindTenant, DisplayName: "Acme", Seed: "another-user"})
		require.NoError(t, err)
		if strategyName != naming.StrategyDisplayName {
			// displayname derives from the display name, so two users asking for the
			// same label legitimately collide on attempt 0 and separate on retry.
			assert.NotEqual(t, first, other, "%s gave two seeds the same name", strategyName)
		}

		// A retry must move, or Seeded would loop on a name it cannot have.
		retry, err := s.Generate(naming.Request{Kind: naming.KindTenant, DisplayName: "Acme", Seed: "user-digest", Attempt: 1})
		require.NoError(t, err)
		assert.NotEqual(t, first, retry, "%s repeated a name on a seeded retry", strategyName)
	}
}

// A Tenant and a Project seeded from the same User must not collide:
// they are different objects and one would otherwise adopt the other's name.
func TestSeededNamesDifferByKind(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	tenant, err := s.Generate(naming.Request{Kind: naming.KindTenant, Seed: "u"})
	require.NoError(t, err)
	proj, err := s.Generate(naming.Request{Kind: naming.KindProject, Seed: "u"})
	require.NoError(t, err)

	assert.NotEqual(t, tenant, proj)
}

// Seeded keeps trying while claim reports the candidate belongs to somebody
// else, and stops the moment one is ours. With a small name space that is not a
// theoretical path — two users derive the same word pair routinely.
func TestSeededSkipsNamesOwnedByAnotherUser(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	var tried []string
	name, err := naming.Seeded(s, naming.Request{Kind: naming.KindTenant, Seed: "u"},
		func(candidate string) (bool, error) {
			tried = append(tried, candidate)
			return len(tried) > 2, nil
		})

	require.NoError(t, err)
	assert.Len(t, tried, 3)
	assert.Equal(t, tried[2], name)
}

// A Seed is mandatory: without one, Generate is random and the retry loop would
// never converge on the object a previous attempt created.
func TestSeededRequiresASeed(t *testing.T) {
	s, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	_, err = naming.Seeded(s, naming.Request{Kind: naming.KindTenant},
		func(string) (bool, error) { return true, nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a Seed")
}
