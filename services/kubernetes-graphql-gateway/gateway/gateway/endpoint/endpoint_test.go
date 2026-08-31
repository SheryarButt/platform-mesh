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

package endpoint

import (
	"testing"

	"github.com/stretchr/testify/require"

	pmgatewayv1alpha1 "go.platform-mesh.io/apis/gateway/v1alpha1"
)

func TestUsesServiceAccountForRequests(t *testing.T) {
	tests := []struct {
		name           string
		metadata       *pmgatewayv1alpha1.ClusterMetadata
		want           bool
		wantErrMessage string
	}{
		{
			name:     "missing metadata uses caller token",
			metadata: nil,
		},
		{
			name:     "omitted mode uses caller token",
			metadata: &pmgatewayv1alpha1.ClusterMetadata{},
		},
		{
			name: "explicit user token mode uses caller token",
			metadata: &pmgatewayv1alpha1.ClusterMetadata{
				RequestIdentityMode: pmgatewayv1alpha1.RequestIdentityModeUserToken,
			},
		},
		{
			name: "service account mode uses service account",
			metadata: &pmgatewayv1alpha1.ClusterMetadata{
				RequestIdentityMode: pmgatewayv1alpha1.RequestIdentityModeServiceAccount,
			},
			want: true,
		},
		{
			name: "unknown mode fails closed",
			metadata: &pmgatewayv1alpha1.ClusterMetadata{
				RequestIdentityMode: pmgatewayv1alpha1.RequestIdentityMode("unknown"),
			},
			wantErrMessage: `unsupported request identity mode "unknown"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := usesServiceAccountForRequests(tt.metadata)
			if tt.wantErrMessage != "" {
				require.EqualError(t, err, tt.wantErrMessage)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
