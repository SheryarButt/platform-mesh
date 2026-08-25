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

package jwt

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

func TestClaimSet_String(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"email":              "email@example.org",
		"mail":               "mail@example.org",
		"preferred_username": "roman",
		"numeric_user_id":    42,
	})
	tokenString, err := token.SignedString(joseTestKey)
	assert.NoError(t, err)

	webToken, err := New(tokenString, signatureAlgorithms)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		claimName string
		want      string
		found     bool
	}{
		{name: "custom string claim", claimName: "preferred_username", want: "roman", found: true},
		{name: "email remains distinct", claimName: "email", want: "email@example.org", found: true},
		{name: "mail remains distinct", claimName: "mail", want: "mail@example.org", found: true},
		{name: "missing claim", claimName: "missing", found: false},
		{name: "non-string claim", claimName: "numeric_user_id", found: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := webToken.Claims.String(tt.claimName)
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.want, got)
		})
	}
}
