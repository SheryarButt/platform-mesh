#!/usr/bin/env bash
# Copyright The Platform Mesh Authors.
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

# Renders the OCM module payloads: the chart, with the values the deployer owns
# at apply time left as CEL expressions.
#
#   ./render.sh [outdir]     default gen/
#
# Two payloads, one per OCMModule component:
#
#   controller-manifests.yaml        the manager        (placement: root-shard)
#   virtualworkspace-manifests.yaml  the tenancy VW     (placement: per-front-proxy,
#                                                        exposed via `mapping`)
#
# Used by BOTH build.sh (local) and .github/workflows/tenancy-operator.yml (the
# released component's resources), so a payload that works locally is the
# payload that ships.

set -euo pipefail

cd "$(dirname "$0")"

OUT=${1:-gen}
mkdir -p "$OUT"

# Helm is only a renderer here: nothing installs this release. The deployer
# applies the objects itself, after interpolating the expressions below.
#
# --namespace is an expression rather than a name, so one packaged component can
# be applied into whatever namespace each OCMModule component declares.
helm template tenancy-operator ../helm/tenancy-operator \
  -f values.module.yaml \
  --namespace '${targetNamespace}' \
  > "$OUT/controller-manifests.yaml"

# The virtual workspace payload: the SAME chart and base values with the VW
# delta layered on top, restricted to the VW's own template — the manager
# objects belong to the controller payload and must not be applied twice.
helm template tenancy-operator ../helm/tenancy-operator \
  -f values.module.yaml \
  -f values.module.virtualworkspace.yaml \
  --show-only templates/virtualworkspace.yaml \
  --namespace '${targetNamespace}' \
  > "$OUT/virtualworkspace-manifests.yaml"

for PAYLOAD in "$OUT/controller-manifests.yaml" "$OUT/virtualworkspace-manifests.yaml"; do
  # `tenancy-operator.image` composes registry/repository:tag, so the chart
  # cannot emit a single CEL expression for the image. It renders the sentinel
  # from values.module.yaml, which becomes the expression here — the payload
  # then reads the image off the component descriptor, and the component version
  # is the only place a tag is written.
  sed -i.bak 's|OCM/IMAGE:REF|${ocm.resources.byName("image").access.imageReference}|g' "$PAYLOAD"
  rm -f "$PAYLOAD.bak"

  grep -q 'byName("image")' "$PAYLOAD" ||
    { echo "image sentinel not found in $PAYLOAD — did the chart's image helper change?" >&2; exit 1; }

  echo "rendered $PAYLOAD"
done
