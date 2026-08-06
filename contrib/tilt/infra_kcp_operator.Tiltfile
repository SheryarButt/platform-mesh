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

# infra_kcp_operator.Tiltfile — the UPSTREAM kcp-operator.
#
# Split out of infra_remote.Tiltfile because it is the one piece of remote infra
# that is not unconditional: the `deployer` profile installs a DIFFERENT
# kcp-operator (the ntnn fork, which splits config/workload controllers and adds
# the Compiled* CRDs the deployer's admin CRs compile into). Both operators own
# the same `operator.kcp.io` CRDs and the same
# kcp-operator-system/kcp-operator-controller-manager Deployment, so installing
# both means whichever applies last wins and the other's controllers reconcile
# against CRDs they do not understand.
#
# Included conditionally from the root Tiltfile — `include()`, unlike `load()`,
# does not have to be top-level.

# kcp-operator — reconciles the RootShard/Shard/FrontProxy CRs that deploy_kcp
# emits. Pinned to a released version.
#
# The operator's own config/manager kustomization pins `newTag: e2e` — a
# floating CI tag, not a release (`:e2e` is not even a stable image). We pull the
# base at the release ref and override the image tag to the same release via a
# generated overlay, so we run a reproducible `:vX.Y.Z` image. `kubectl -k`
# resolves the remote base natively; Tilt's builtin kustomize() does not fetch
# remote URLs. Override the version with KCP_OPERATOR_VERSION.
KCP_OPERATOR_VERSION = os.getenv('KCP_OPERATOR_VERSION', 'v0.8.2')
local_resource(
    'kcp-operator',
    cmd='''set -eo pipefail
tmp=$(mktemp -d)
cat > "$tmp/kustomization.yaml" <<EOF
resources:
  - https://github.com/kcp-dev/kcp-operator/config/default?ref={v}
images:
  - name: ghcr.io/kcp-dev/kcp-operator
    newTag: {v}
EOF
kubectl apply --server-side -k "$tmp"
rm -rf "$tmp"'''.format(v=KCP_OPERATOR_VERSION),
    labels=['infra'],
    allow_parallel=True,
)
