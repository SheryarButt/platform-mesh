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
	"strings"
	"unicode"
)

// StrategyDisplayName is the name of the display-name strategy.
const StrategyDisplayName = "displayname"

// displayNameAttempts caps retries.
const displayNameAttempts = 12

func init() { Register(&displayNameStrategy{}) }

// displayNameStrategy derives the name from the display name the caller asked
// for, disambiguating with a suffix when that slug is taken.
//
// The one strategy where the name carries tenant-supplied meaning, which is a
// real trade: the name becomes a path segment, so the display name ends up in
// kubeconfig URLs, logs and errors. The slug is also a snapshot — it does not
// follow a later rename, because renaming would move the workspace.
//
// Most defensible for Projects, unique only within one Tenant; least for
// Tenants, where the first tenant to create "platform" holds that name
// against everyone.
type displayNameStrategy struct{}

func (s *displayNameStrategy) Name() string { return StrategyDisplayName }

func (s *displayNameStrategy) Generate(req Request) (string, error) {
	if req.Attempt >= displayNameAttempts {
		return "", ErrExhausted
	}

	slug := Slugify(req.DisplayName)
	if slug == "" {
		// No usable display name — an empty one, or one written entirely in a
		// script that leaves nothing after slugification. Fall back to a word pair
		// rather than failing the create: the caller asked for a Project, not for
		// a naming lecture, and a nameless object cannot exist.
		return (&wordsStrategy{}).Generate(req)
	}

	if req.Attempt == 0 {
		return slug, nil
	}

	// Suffix on every retry. The slug itself cannot vary — it is derived — so
	// without this the strategy would return the same taken name forever.
	buf, err := Entropy(req)
	if err != nil {
		return "", err
	}
	tail := suffix(buf, 4)
	return truncate(slug, MaxNameLength-1-len(tail)) + "-" + tail, nil
}

// Slugify reduces arbitrary text to a DNS-1123 label, or "" if nothing usable
// survives.
//
// Exported because an external strategy deriving names from some other
// human-supplied string wants exactly this, and reimplementing it is how two
// strategies end up disagreeing about what a valid name looks like.
func Slugify(s string) string {
	var b strings.Builder
	prevHyphen := false

	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', unicode.IsDigit(r) && r < 128:
			b.WriteRune(r)
			prevHyphen = false
		default:
			// Every run of anything else — spaces, punctuation, non-ASCII — becomes
			// exactly one hyphen. Collapsing here rather than in a second pass is
			// what keeps "Acme  &  Co." from turning into "acme----co".
			if b.Len() > 0 && !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	out = truncate(out, MaxNameLength)

	// A DNS-1123 label must start and end alphanumeric. Truncation can leave a
	// trailing hyphen, and a display name starting with a digit is fine, so only
	// the hyphens need trimming again.
	return strings.Trim(out, "-")
}

func truncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max]
}
