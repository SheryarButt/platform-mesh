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

package authn

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestNoopValidatorAcceptsAnyToken(t *testing.T) {
	var v Validator = NoopValidator{}
	for _, tok := range []string{"", "bogus", "header.payload.sig"} {
		ok, err := v.Validate(context.Background(), tok)
		assert.NoError(t, err)
		assert.True(t, ok, "NoopValidator must accept token %q", tok)
	}
}

func fakeClientset(authenticated bool, calls *atomic.Int32, returnErr error) *fake.Clientset {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		if returnErr != nil {
			return true, nil, returnErr
		}
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status.Authenticated = authenticated
		return true, tr, nil
	})
	return cs
}

func TestValidToken(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 5*time.Minute)

	ok, err := v.Validate(t.Context(), "valid-token")

	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int32(1), calls.Load())
}

func TestInvalidToken(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(false, &calls, nil), 5*time.Minute)

	ok, err := v.Validate(t.Context(), "invalid-token")

	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load())
}

func TestAPIError(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(false, &calls, fmt.Errorf("connection refused")), 5*time.Minute)

	ok, err := v.Validate(t.Context(), "some-token")

	assert.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load())
}

func TestCacheHit(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 5*time.Minute)

	ok, err := v.Validate(t.Context(), "cached-token")
	assert.NoError(t, err)
	assert.True(t, ok)

	ok, err = v.Validate(t.Context(), "cached-token")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int32(1), calls.Load(), "second call should use cache")
}

func TestCacheExpiry(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 50*time.Millisecond)

	_, _ = v.Validate(t.Context(), "expiring-token")
	assert.Equal(t, int32(1), calls.Load())

	_, _ = v.Validate(t.Context(), "expiring-token")
	assert.Equal(t, int32(1), calls.Load(), "should use cache before expiry")

	time.Sleep(100 * time.Millisecond)

	_, _ = v.Validate(t.Context(), "expiring-token")
	assert.Equal(t, int32(2), calls.Load(), "should call API after expiry")
}

func TestCacheStoresInvalidResult(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(false, &calls, nil), 5*time.Minute)

	ok, _ := v.Validate(t.Context(), "bad-token")
	assert.False(t, ok)

	ok, _ = v.Validate(t.Context(), "bad-token")
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load(), "invalid result should also be cached")
}

func TestAPIErrorNotCached(t *testing.T) {
	var callIdx atomic.Int32
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		idx := callIdx.Add(1)
		if idx == 1 {
			return true, nil, fmt.Errorf("transient error")
		}
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status.Authenticated = true
		return true, tr, nil
	})
	v := NewTokenReviewValidatorFromClientset(cs, 5*time.Minute)

	ok, err := v.Validate(t.Context(), "retry-token")
	assert.Error(t, err)
	assert.False(t, ok)

	ok, err = v.Validate(t.Context(), "retry-token")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int32(2), callIdx.Load())
}

func TestStartStopsOnCancel(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 50*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		v.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not exit after context cancellation")
	}
}

func TestCacheDisabledWhenTTLZero(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 0)

	_, _ = v.Validate(t.Context(), "same-token")
	_, _ = v.Validate(t.Context(), "same-token")
	assert.Equal(t, int32(2), calls.Load(), "every call should hit the API when caching is disabled")
}

func TestCacheTTLCappedAtTokenExpiry(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Second)),
	})
	shortLivedToken, err := token.SignedString([]byte("test-secret"))
	assert.NoError(t, err)

	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 5*time.Minute)

	_, _ = v.Validate(t.Context(), shortLivedToken)
	assert.Equal(t, int32(1), calls.Load())

	_, _ = v.Validate(t.Context(), shortLivedToken)
	assert.Equal(t, int32(1), calls.Load(), "should use cache before token expiry")

	time.Sleep(1500 * time.Millisecond)

	_, _ = v.Validate(t.Context(), shortLivedToken)
	assert.Equal(t, int32(2), calls.Load(), "should call API after token expired")
}

func TestNegativeVerdictNotCachedLong(t *testing.T) {
	old := negativeCacheTTL
	negativeCacheTTL = 50 * time.Millisecond
	t.Cleanup(func() { negativeCacheTTL = old })

	var callIdx atomic.Int32
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		idx := callIdx.Add(1)
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status.Authenticated = idx > 1 // first review denies, later ones allow
		if idx == 1 {
			tr.Status.Error = "token not yet valid"
		}
		return true, tr, nil
	})
	v := NewTokenReviewValidatorFromClientset(cs, 5*time.Minute)

	ok, _ := v.Validate(t.Context(), "recovering-token")
	assert.False(t, ok)

	// Within the negative TTL the deny verdict is served from cache.
	ok, _ = v.Validate(t.Context(), "recovering-token")
	assert.False(t, ok)
	assert.Equal(t, int32(1), callIdx.Load())

	time.Sleep(100 * time.Millisecond)

	// After the short negative TTL a fresh review runs and succeeds.
	ok, err := v.Validate(t.Context(), "recovering-token")
	assert.NoError(t, err)
	assert.True(t, ok, "deny verdict must not outlive the negative TTL")
	assert.Equal(t, int32(2), callIdx.Load())
}

func TestDeniedVerdictIsLogged(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logger := funcr.New(func(prefix, args string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, args)
	}, funcr.Options{Verbosity: 5})
	ctx := log.IntoContext(t.Context(), logger)

	var calls atomic.Int32
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status.Authenticated = false
		tr.Status.Error = "square/go-jose: error in cryptographic primitive"
		return true, tr, nil
	})
	v := NewTokenReviewValidatorFromClientset(cs, 5*time.Minute)

	_, _ = v.Validate(ctx, "bad-token") // fresh denial
	_, _ = v.Validate(ctx, "bad-token") // cached denial

	mu.Lock()
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	mu.Unlock()
	assert.Contains(t, joined, "square/go-jose", "fresh denial must log tr.Status.Error")
	assert.Contains(t, joined, "cached", "cached denial must be distinguishable from a fresh review")
}

func TestStaleCacheMissUsesCachedVerdict(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 5*time.Minute)
	const token = "stale-miss-token"

	ok, err := v.Validate(t.Context(), token)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int32(1), calls.Load())

	// Simulate a caller that observed a cache miss before the first validation
	// populated the cache, but reached singleflight only after it completed.
	ok, err = v.validateAfterCacheMiss(t.Context(), token, hashToken(token))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int32(1), calls.Load(), "stale cache miss should not trigger another TokenReview")
}

func TestConcurrentValidation(t *testing.T) {
	var calls atomic.Int32
	v := NewTokenReviewValidatorFromClientset(fakeClientset(true, &calls, nil), 5*time.Minute)

	const goroutines = 20
	errCh := make(chan error, goroutines)
	start := make(chan struct{})

	for range goroutines {
		go func() {
			<-start
			ok, err := v.Validate(t.Context(), "concurrent-token")
			if err != nil {
				errCh <- err
				return
			}
			if !ok {
				errCh <- fmt.Errorf("expected authenticated=true")
				return
			}
			errCh <- nil
		}()
	}
	close(start)

	for range goroutines {
		assert.NoError(t, <-errCh)
	}

	// The cache recheck inside singleflight covers callers that observed the
	// initial cache miss but arrive after the first flight has completed.
	assert.Equal(t, int32(1), calls.Load(), "concurrent calls should share one TokenReview")
}
