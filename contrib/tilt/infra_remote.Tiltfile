# Copyright 2026 The Platform Mesh Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# infra_remote.Tiltfile — infrastructure that requires fetching remote charts
# and Tilt extensions. Split out of the main Tiltfile so `TILT_NO_INFRA=1` can
# render the local manifests + kcp CRs entirely offline (the ext:// loads below
# fetch at eval time).

load('ext://cert_manager', 'deploy_cert_manager')
load('ext://helm_remote', 'helm_remote')

# cert-manager — issues the self-signed CAs dex and the kcp shards root from.
deploy_cert_manager(version='v1.19.2')

# Envoy Gateway controller — programs the `eg` Gateway the deployer brings
# (config/bases/envoy-gateway). Production uses traefik; envoy here is enough to
# prove the routing, and it carries every SNI name in the environment.
namespace_yaml = 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: envoy-gateway-system\n'
k8s_yaml(blob(namespace_yaml))
helm_remote(
    'gateway-helm',
    repo_url='oci://registry-1.docker.io/envoyproxy',
    repo_name='gateway-helm',
    release_name='envoy',
    namespace='envoy-gateway-system',
    version='v1.7.0',
)

# kcp-operator is NOT here, and is not deployed at all. platform-mesh-deployer
# runs kcp-operator's config and workload controller groups inside its own
# manager and installs only their CRDs.

# ---------------------------------------------------------------------------
# Delivery engines for the provider-operator's ManagedProvider deploy step
# ---------------------------------------------------------------------------
# ManagedProvider.Deploy emits Flux objects; the two RuntimeDeployment types are:
#   - flux: source.toolkit.fluxcd.io (HelmRepository/OCIRepository) + a
#           helm.toolkit.fluxcd.io/HelmRelease            → needs Flux only
#   - ocm:  delivery.ocm.software (Repository/Component/Resource) resolved by the
#           OCM controller, handed off to a Flux HelmRelease → needs OCM + Flux
# Both are installed here so either RuntimeDeployment type works out of the box.
# This is the deliberate ADR-02 divergence: a delivery engine lives in the dev
# env so ManagedProvider can drive deploys (versions mirror helm-charts/local-setup).

# Flux — source + helm (+ kustomize) controllers. Image-automation, reflection
# and notification controllers are disabled (unused by ManagedProvider).
k8s_yaml(blob('apiVersion: v1\nkind: Namespace\nmetadata:\n  name: flux-system\n'))
helm_remote(
    'flux2',
    repo_url='oci://ghcr.io/fluxcd-community/charts',
    repo_name='flux2',
    release_name='flux',
    namespace='flux-system',
    version='2.17.2',
    set=[
        'imageAutomationController.create=false',
        'imageReflectionController.create=false',
        'notificationController.create=false',
    ],
)

# OCM controller (ocm-k8s-toolkit) — reconciles delivery.ocm.software CRs for the
# ManagedProvider `ocm` deploy type. Only needed for that type; the `flux` type
# runs on Flux alone. The chart artifact is literally named `chart` at this OCI path.
k8s_yaml(blob('apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ocm-system\n'))
helm_remote(
    'chart',
    repo_url='oci://ghcr.io/open-component-model/kubernetes/controller',
    repo_name='ocm-k8s-toolkit',
    release_name='ocm-k8s-toolkit',
    namespace='ocm-system',
    version='0.3.0',
)

# NO INGRESS PORT-FORWARD any more.
#
# It existed because *.pm.localhost resolves to loopback, so the host could only
# reach the gateway through a tunnel. Every hostname is now
# <name>.<node-ip>.sslip.io on the gateway's pinned NodePort, which the host
# resolves and routes to directly — so there is nothing to forward, and no
# long-running process whose death silently breaks every kubectl.
