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

// Package naming mints the server-assigned metadata.name of Tenants and
// Projects.
//
// The name is also the kcp Workspace name, and therefore a path segment: that is
// why it is server-assigned and a client cannot supply it. Configurable here is
// only WHICH server-assigned name.
//
// A strategy is chosen at boot and applies to names minted afterwards. Existing
// names are never rewritten — changing one would move a workspace — so switching
// leaves a cluster with a mix, by design.
//
// Only for names a client asks for. Objects a reconciler creates are named
// deterministically from their content (see pkg/membership), because there a
// retry must land on the SAME object rather than mint a second one.
package naming

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Kind identifies what is being named, so one strategy can behave differently
// per resource without needing a separate implementation for each.
type Kind string

const (
	// KindTenant names a Tenant. These live in the single directory
	// workspace, so the name must be unique across the WHOLE platform.
	KindTenant Kind = "Tenant"

	// KindProject names a Project. These live in their Tenant's workspace,
	// so the name need only be unique within one Tenant — which is what
	// makes a display-name strategy tolerable here and risky above.
	KindProject Kind = "Project"
)

// Request is everything a strategy may base a name on.
type Request struct {
	// Kind is the resource being named.
	Kind Kind

	// DisplayName is the human label the caller asked for. May be empty; a
	// strategy that needs it must cope with that rather than fail the create.
	DisplayName string

	// Attempt is 0 on the first try and increments after each name that turned
	// out to be taken. A strategy that cannot vary its output returns
	// ErrExhausted once Attempt exceeds what it can offer.
	Attempt int

	// Seed makes generation DETERMINISTIC: with a Seed set, Generate must be a
	// pure function of this Request, returning the same name for the same
	// (Seed, Attempt, Kind) every time and in every process.
	//
	// That is what lets a reconciler name an object it creates on someone's
	// behalf. Creating the object and recording a pointer to it are two writes
	// that cannot be made atomic, so anything that requeues in between must
	// recompute the SAME name and adopt what is already there — a random name
	// would mint a second object granting the same thing.
	//
	// Empty for anything a client asks for, where the name should be
	// unguessable. See Seeded.
	Seed string
}

// ErrExhausted says a strategy has no further candidate for this Request.
//
// Returned rather than looping forever, because for a deterministic strategy a
// taken name is a permanent answer, not a transient one — the caller should
// surface a conflict to the client instead of retrying.
var ErrExhausted = errors.New("no further name candidates")

// Strategy mints names.
//
// Implementations live outside this package as often as inside it: the interface
// is small and stable precisely so an installation can supply its own — a name
// drawn from an external IPAM, a corporate naming scheme, a reserved-word block
// list — by implementing this and calling Register from an init function.
type Strategy interface {
	// Name is how the strategy is selected in configuration. Lowercase, stable:
	// it appears in a flag value and in Helm values, so renaming it breaks
	// deployments.
	Name() string

	// Generate returns one candidate. It must not consult the API server; the
	// caller owns collision detection and retry, so a strategy stays a pure
	// function of its Request plus whatever randomness it wants.
	//
	// When req.Seed is set the randomness must come from Entropy, which derives
	// it from the Seed — see Request.Seed. A strategy that ignores this and reads
	// crypto/rand anyway will duplicate objects on retry rather than adopt them.
	//
	// The returned name MUST satisfy Validate. A strategy that cannot produce a
	// valid name for a given Request returns an error rather than a name that
	// fails at create time with an opaque apiserver validation message.
	Generate(req Request) (string, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Strategy{}
)

// Register adds a strategy to the registry.
//
// Panics on a duplicate name. Registration happens from init functions, so a
// clash is a build-time mistake — two packages claiming one name — and silently
// letting the last one win would mean the strategy a deployment gets depends on
// import order.
func Register(s Strategy) {
	registryMu.Lock()
	defer registryMu.Unlock()

	name := s.Name()
	if name == "" {
		panic("naming: a strategy must have a name")
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("naming: strategy %q registered twice", name))
	}
	registry[name] = s
}

// Get resolves a configured strategy name.
//
// The error lists what IS available, because the common failure is a typo or a
// strategy whose package was never imported — and "unknown strategy" alone
// cannot tell those apart.
func Get(name string) (Strategy, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown naming strategy %q: registered strategies are %s",
			name, strings.Join(registeredLocked(), ", "))
	}
	return s, nil
}

// Registered lists the available strategy names, sorted.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registeredLocked()
}

func registeredLocked() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MaxNameLength caps a generated name.
//
// A DNS-1123 label allows 63, but this name becomes a kcp Workspace name and
// then a path segment, and paths are concatenated, logged and put in kubeconfig
// server URLs. 48 leaves room for that without being tight for any strategy
// here; a display name longer than this is truncated, not rejected.
const MaxNameLength = 48

// Validate reports whether a generated name is usable as an object and workspace
// name.
//
// Enforced centrally rather than trusted per strategy: an external strategy is
// exactly the code this package cannot review, and a name that fails here fails
// loudly at generation instead of as an apiserver rejection halfway through a
// create.
func Validate(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > MaxNameLength {
		return fmt.Errorf("name %q is %d characters; the limit is %d", name, len(name), MaxNameLength)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("name %q is not a valid DNS-1123 label: %s", name, strings.Join(errs, "; "))
	}
	return nil
}

// Apply mints a name, hands it to create, and retries while the name is taken.
//
// The retry loop lives here rather than at each call site so that "what happens
// when a name collides" has exactly one answer. taken decides whether an error
// from create means "that name is in use" — callers pass apierrors.IsAlreadyExists;
// any other error is returned untouched, because a permission or connectivity
// failure must not be retried as though it were a collision.
//
// Returns the name that was actually created.
func Apply(s Strategy, req Request, create func(name string) error, taken func(error) bool) (string, error) {
	for attempt := 0; ; attempt++ {
		req.Attempt = attempt

		name, err := s.Generate(req)
		if err != nil {
			if errors.Is(err, ErrExhausted) && attempt > 0 {
				// Out of candidates after at least one collision: the name the
				// client effectively asked for is taken. Report that, not the
				// exhaustion, which is an implementation detail of the strategy.
				return "", fmt.Errorf("naming strategy %q: every candidate name was already in use: %w", s.Name(), err)
			}
			return "", fmt.Errorf("naming strategy %q: %w", s.Name(), err)
		}
		if err := Validate(name); err != nil {
			return "", fmt.Errorf("naming strategy %q produced an unusable name: %w", s.Name(), err)
		}

		err = create(name)
		if err == nil {
			return name, nil
		}
		if !taken(err) {
			return "", err
		}
	}
}

// Entropy returns the randomness for one Generate call.
//
// Cryptographic when the Request has no Seed, and a hash of the Seed when it
// does. Every strategy draws from here rather than from crypto/rand directly, so
// determinism is a property of the package instead of something each strategy
// has to remember — the failure mode of forgetting is a duplicate object, and it
// only shows up under a retry nobody reproduces on purpose.
//
// 32 bytes: more than any built-in strategy needs, so none of them has to
// re-derive.
func Entropy(req Request) ([]byte, error) {
	if req.Seed == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			// Never fall back to a weaker source: a predictable name here is a
			// guessable workspace path.
			return nil, fmt.Errorf("reading randomness: %w", err)
		}
		return buf, nil
	}

	// Attempt and Kind are in the digest so a collision resolves to a different
	// name, and so a Tenant and a Project seeded from the same user do
	// not collide with each other.
	sum := sha256.Sum256([]byte(strings.Join([]string{
		req.Seed, strconv.Itoa(req.Attempt), string(req.Kind),
	}, "\n")))
	return sum[:], nil
}

// Seeded resolves the name of an object the platform creates on a user's behalf,
// and is the deterministic counterpart to Apply.
//
// claim tries one candidate and reports whether the object is now OURS — created,
// or already present and belonging to us. False means the name is taken by
// somebody else and the next candidate should be tried.
//
// That ownership test is not optional. Apply can treat any AlreadyExists as
// "pick another name" because it is naming a brand-new object; here the whole
// point is to adopt an object a previous attempt may have created, and a strategy
// with a small name space will hand two different users the same candidate. Without
// the test, the second user would silently adopt the first user's Tenant.
func Seeded(s Strategy, req Request, claim func(name string) (bool, error)) (string, error) {
	if req.Seed == "" {
		return "", errors.New("naming: a seeded name requires a Seed, or retries will not converge")
	}

	for attempt := 0; ; attempt++ {
		req.Attempt = attempt

		name, err := s.Generate(req)
		if err != nil {
			return "", fmt.Errorf("naming strategy %q: %w", s.Name(), err)
		}
		if err := Validate(name); err != nil {
			return "", fmt.Errorf("naming strategy %q produced an unusable name: %w", s.Name(), err)
		}

		ours, err := claim(name)
		if err != nil {
			return "", err
		}
		if ours {
			return name, nil
		}
	}
}
