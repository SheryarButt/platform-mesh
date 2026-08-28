//go:build e2e

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

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	pmcorev1alpha1 "go.platform-mesh.io/apis/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestKindSuite(t *testing.T) {
	suite.Run(t, new(KindTestSuite))
}

func (s *KindTestSuite) Test01ResourceReady() {
	ctx := s.T().Context()

	s.Eventually(func() bool {
		pm := pmcorev1alpha1.PlatformMesh{}
		err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{
			Name:      "platform-mesh",
			Namespace: "platform-mesh-system",
		}, &pm)
		if err != nil {
			s.logger.Warn().Err(err).Msg("Failed to get Platform Mesh resource")
			return false
		}

		for _, condition := range pm.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				s.logger.Info().Msg("PlatformMesh resource is ready")
				return true
			}
		}
		return false
	}, 25*time.Minute, 10*time.Second)
}

func (s *KindTestSuite) Test02ExtraWorkspaces() {
	ctx := s.T().Context()

	pm := pmcorev1alpha1.PlatformMesh{}
	err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{
		Name:      "platform-mesh",
		Namespace: "platform-mesh-system",
	}, &pm)
	s.Assert().NoError(err, "Failed to get Platform Mesh resource")

	pm.Spec.Kcp.ExtraWorkspaces = []pmcorev1alpha1.WorkspaceDeclaration{
		{
			Path: "root:orgs:extra1",
			Type: pmcorev1alpha1.WorkspaceTypeReference{
				Name: "org",
				Path: "root",
			},
		},
	}
	pm.Spec.Kcp.ExtraProviderConnections = append(pm.Spec.Kcp.ExtraProviderConnections,
		pmcorev1alpha1.ProviderConnection{
			Path:      "root:orgs:extra1",
			Secret:    "extra1-kubeconfig",
			External:  true,
			AdminAuth: ptr.To(true),
		},
	)
	s.logger.Info().Str("platformmesh", fmt.Sprintf("%+v", pm)).Msg("Updating Platform Mesh resource to add extra workspace and provider connection")
	err = s.client.Update(ctx, &pm)
	s.Assert().NoError(err, "Failed to update Platform Mesh resource")

	s.Eventually(func() bool {
		updatedPM := pmcorev1alpha1.PlatformMesh{}
		err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{
			Name:      "platform-mesh",
			Namespace: "platform-mesh-system",
		}, &updatedPM)
		if err != nil {
			s.logger.Warn().Err(err).Msg("Failed to get Platform Mesh resource")
			return false
		}

		for _, condition := range updatedPM.Status.Conditions {
			if condition.Status != "True" {
				s.logger.Info().Msg("PlatformMesh resource is not ready")
				return false
			}
		}

		// get extra1 secret
		secret := &corev1.Secret{}
		err = s.client.Get(ctx, ctrlruntimeclient.ObjectKey{
			Name:      "extra1-kubeconfig",
			Namespace: "platform-mesh-system",
		}, secret)
		if err != nil {
			s.logger.Warn().Err(err).Msg("Failed to get extra1-kubeconfig secret")
			return false
		}

		s.logger.Info().Msg("PlatformMesh resource is ready and extra1-kubeconfig secret exists")
		return true
	}, 20*time.Minute, 10*time.Second)
}
