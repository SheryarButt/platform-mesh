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
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"

	"go.platform-mesh.io/kubernetes-graphql-gateway/gateway/gateway/metrics"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Validator validates bearer tokens.
type Validator interface {
	Validate(ctx context.Context, token string) (bool, error)
}

// NoopValidator accepts every token. Intended for embedded deployments where
// the caller has already authenticated the request before reaching the gateway
// (e.g. an outer mux that performs its own auth and injects the token via
// utilscontext.SetToken purely for upstream proxying). Do not use as the
// front-line validator on internet-facing deployments.
type NoopValidator struct{}

// Validate always reports the token as authenticated.
func (NoopValidator) Validate(_ context.Context, _ string) (bool, error) {
	return true, nil
}

const maxCacheSize = 10000

// negativeCacheTTL bounds how long a denied (authenticated:false) verdict may
// be served from cache. Deny verdicts are often transient (token not yet
// propagated, clock skew, revoked-then-reissued sessions) and must not stick
// for the full positive cacheTTL. Kept as a var so tests can shrink it.
var negativeCacheTTL = 2 * time.Second

// TokenReviewValidator validates tokens via the Kubernetes TokenReview API.
type TokenReviewValidator struct {
	clientset kubernetes.Interface
	cache     *ttlcache.Cache[string, bool]
	cacheTTL  time.Duration
	inflight  singleflight.Group
	metrics   *metrics.AuthMetrics
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

var jwtParser = jwt.NewParser(jwt.WithoutClaimsValidation())

func tokenExpiry(token string) time.Time {
	claims := &jwt.RegisteredClaims{}
	if _, _, err := jwtParser.ParseUnverified(token, claims); err != nil {
		return time.Time{}
	}
	if claims.ExpiresAt == nil {
		return time.Time{}
	}
	return claims.ExpiresAt.Time
}

func newCache(ttl time.Duration) *ttlcache.Cache[string, bool] {
	if ttl <= 0 {
		return nil
	}
	return ttlcache.New(
		ttlcache.WithTTL[string, bool](ttl),
		ttlcache.WithCapacity[string, bool](maxCacheSize),
	)
}

// NewTokenReviewValidator creates a validator that calls TokenReview on the
// given cluster. If cacheTTL <= 0, caching is disabled and every request
// triggers an API call.
func NewTokenReviewValidator(cfg *rest.Config, cacheTTL time.Duration, m *metrics.AuthMetrics) (*TokenReviewValidator, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &TokenReviewValidator{
		clientset: cs,
		cache:     newCache(cacheTTL),
		cacheTTL:  cacheTTL,
		metrics:   m,
	}, nil
}

// NewTokenReviewValidatorFromClientset creates a validator from an existing
// clientset — useful for testing.
func NewTokenReviewValidatorFromClientset(cs kubernetes.Interface, cacheTTL time.Duration) *TokenReviewValidator {
	return &TokenReviewValidator{
		clientset: cs,
		cache:     newCache(cacheTTL),
		cacheTTL:  cacheTTL,
	}
}

func (v *TokenReviewValidator) cachedVerdict(ctx context.Context, key string) (bool, bool) {
	if v.cache == nil {
		return false, false
	}

	item := v.cache.Get(key)
	if item == nil {
		return false, false
	}

	authenticated := item.Value()
	if v.metrics != nil {
		labelResult := metrics.ResultDenied
		if authenticated {
			labelResult = metrics.ResultAllowed
		}
		v.metrics.RecordCacheHit(labelResult)
	}
	if !authenticated {
		log.FromContext(ctx).V(1).Info("token denied (cached TokenReview verdict)")
	}
	return authenticated, true
}

func (v *TokenReviewValidator) Validate(ctx context.Context, token string) (bool, error) {
	key := hashToken(token)
	if result, ok := v.cachedVerdict(ctx, key); ok {
		return result, nil
	}

	return v.validateAfterCacheMiss(ctx, token, key)
}

func (v *TokenReviewValidator) validateAfterCacheMiss(ctx context.Context, token, key string) (bool, error) {
	result, err, _ := v.inflight.Do(key, func() (any, error) {
		// A caller can observe a cache miss and then reach singleflight after a
		// previous flight has populated the cache and completed. Recheck here so
		// that stale miss does not trigger another TokenReview API call.
		if result, ok := v.cachedVerdict(ctx, key); ok {
			return result, nil
		}

		start := time.Now()
		tr, err := v.clientset.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
			Spec: authenticationv1.TokenReviewSpec{Token: token},
		}, metav1.CreateOptions{})
		if err != nil {
			log.FromContext(ctx).Error(err, "TokenReview API call failed")
			if v.metrics != nil {
				v.metrics.RecordAPICall(metrics.ResultError, time.Since(start))
			}
			return false, err
		}

		if !tr.Status.Authenticated {
			log.FromContext(ctx).Info("TokenReview denied token",
				"error", tr.Status.Error, "cache", "fresh")
		}

		if v.cache != nil {
			itemTTL := ttlcache.DefaultTTL
			if !tr.Status.Authenticated {
				// Deny verdicts get a short TTL: enough to absorb request
				// bursts (singleflight already dedupes concurrent ones), but
				// short enough that a token that becomes valid (propagation
				// delay, clock skew) is not rejected for the full cacheTTL.
				itemTTL = min(v.cacheTTL, negativeCacheTTL)
			} else if exp := tokenExpiry(token); !exp.IsZero() {
				if remaining := time.Until(exp); remaining > 0 {
					itemTTL = min(v.cacheTTL, remaining)
				}
			}
			v.cache.Set(key, tr.Status.Authenticated, itemTTL)
		}

		if v.metrics != nil {
			labelResult := metrics.ResultAllowed
			if !tr.Status.Authenticated {
				labelResult = metrics.ResultDenied
			}
			v.metrics.RecordAPICall(labelResult, time.Since(start))
		}

		return tr.Status.Authenticated, nil
	})

	return result.(bool), err
}

// Start begins automatic cache cleanup. Blocks until ctx is cancelled.
func (v *TokenReviewValidator) Start(ctx context.Context) {
	if v.cache == nil {
		return
	}
	go v.cache.Start()
	<-ctx.Done()
	v.cache.Stop()
}
