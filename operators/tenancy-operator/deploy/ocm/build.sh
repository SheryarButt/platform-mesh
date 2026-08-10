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

# Builds the tenancy-operator component LOCALLY, with the same resources the
# release workflow publishes, so a module can be tested before a version is
# tagged. The released component is built by .github/workflows/tenancy-operator.yml
# — this is the dev path, and carries no SBOMs and no signature.
#
#   ./build.sh                                   # build only, into ./ctf
#   ./build.sh --push ghcr.io/platform-mesh/dev  # build and transfer
#
# Env:
#   VERSION        component version to build   (default v0.0.0-dev)
#   IMAGE_VERSION  released image tag to run    (default v0.0.1)
#   IMAGE          full image reference         (default derived from IMAGE_VERSION)
#
# VERSION defaults to a -dev version deliberately: this component is unsigned,
# and pushing it over a released version would replace a signed component with
# one nothing can verify.

set -euo pipefail

cd "$(dirname "$0")"

VERSION=${VERSION:-v0.0.0-dev}
IMAGE_VERSION=${IMAGE_VERSION:-v0.0.1}
IMAGE=${IMAGE:-ghcr.io/platform-mesh/platform-mesh/tenancy-operator:$IMAGE_VERSION}
CTF=${CTF:-./ctf}

PUSH=""
if [ "${1:-}" = "--push" ]; then
  PUSH=${2:?"--push needs an OCM repository, e.g. ghcr.io/platform-mesh/dev"}
fi

rm -rf "$CTF" gen
./render.sh gen

ocm add componentversions --create --file "$CTF" \
  --templater subst \
  component-constructor.yaml \
  -- \
  VERSION="$VERSION" \
  IMAGE_VERSION="$IMAGE_VERSION" \
  IMAGE="$IMAGE" \
  MANIFESTS=gen/controller-manifests.yaml \
  MANIFESTS_VW=gen/virtualworkspace-manifests.yaml

echo "built $CTF: github.com/platform-mesh/images/tenancy-operator:$VERSION"

if [ -n "$PUSH" ]; then
  # NOT --copy-resources. That copies the image into the target registry and
  # REWRITES the resource's access.imageReference to the copy — scheme included,
  # so a plain-HTTP dev registry yields `http://host:port/...` as an image
  # reference and every pod lands in InvalidImageName. The released component
  # keeps the image at ghcr, and so must this one.
  ocm transfer ctf --overwrite "$CTF" "$PUSH"
  echo "pushed to $PUSH"
fi
