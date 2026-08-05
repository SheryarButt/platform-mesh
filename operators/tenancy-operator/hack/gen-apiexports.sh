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

# Render the tenancy APIResourceSchemas and the FOUR APIExports.
#
# apigen emits one APIExport per API group, but this group is deliberately served
# by four exports that differ only in name. Two carry schemas and are split by
# AUDIENCE; two carry only claims and are split by CAPABILITY:
#
#   tenancy               memberships                          -> every Tenant workspace
#   tenancy-platform      users, tenants, indices        -> the directory only
#   tenancy-provisioner   (no schemas) claim: workspaces       -> fleet root + every Tenant
#   tenancy-access        (no schemas) claims: ns, SAs, RBAC   -> every child workspace
#
# An APIBinding imports ALL of an export's resources — there is no subsetting — so
# a single export would make `users` servable in any workspace that binds it to
# store its Memberships. The split is what turns "a tenant workspace cannot serve a
# User" from a rule someone has to remember into a property of the API surface.
#
# So: run apigen twice over disjoint CRD sets, rename each generated export, and
# ship the two schema-less exports as static manifests.
#
# Output goes into deploy/kcp, which the operator binary EMBEDS (see deploy/embed.go) —
# regenerating the API and rebuilding the installer are then the same act, and
# there is no way to run one version of the installer against another version of
# the schemas it installs.

set -euo pipefail

cd "$(dirname "$0")/.."

APIGEN="${APIGEN:?set APIGEN to the apigen binary}"
HEADER="${HEADER:?set HEADER to the yaml boilerplate file}"
GROUP="tenancy.platform-mesh.io"
CRD_DIR="config/crd"
OUT_DIR="deploy/kcp/resources"

# Which CRDs back which schema-bearing export. Split by audience.
PLATFORM_CRDS=(users tenants usermembershipindices)
TENANT_CRDS=(memberships projects)

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

render() {
  local export_name="$1"; shift
  local crds=("$@")

  local in="$work/$export_name/in" out="$work/$export_name/out"
  mkdir -p "$in" "$out"
  for c in "${crds[@]}"; do
    cp "$CRD_DIR/${GROUP}_${c}.yaml" "$in/"
  done

  "$APIGEN" --input-dir "$in" --output-dir "$out" --header-file "$HEADER"

  # apigen names the export after the group; we name it after the audience.
  # Only metadata.name changes — every schema reference inside stays as generated,
  # so the version hashes are never hand-maintained.
  sed "s|^  name: ${GROUP}\$|  name: ${export_name}|" \
    "$out/apiexport-${GROUP}.yaml" > "$OUT_DIR/apiexport-${export_name}.yaml"

  cp "$out"/apiresourceschema-*.yaml "$OUT_DIR/"

  # Rename every schema after a hash of its own CONTENT.
  #
  # apigen names them v<date>-<git-hash>, which is stable across an uncommitted
  # schema change — and APIResourceSchemas are IMMUTABLE, so the installer's
  # create() then hits AlreadyExists and silently keeps serving the old schema.
  # The symptom appears far away: objects rejected for a field the source no
  # longer has. Hashing the content makes a changed schema a different object,
  # which is the only thing an immutable resource can respond to.
  python3 - "$OUT_DIR" "$export_name" <<'PYEOF'
import hashlib, pathlib, re, sys

out_dir, export_name = pathlib.Path(sys.argv[1]), sys.argv[2]
export_file = out_dir / f"apiexport-{export_name}.yaml"
renames = {}

for f in sorted(out_dir.glob("apiresourceschema-*.yaml")):
    text = f.read_text()
    m = re.search(r"^  name: (\S+)$", text, re.M)
    if not m:
        continue
    old = m.group(1)
    # Hash everything except the name line, so the hash describes the schema
    # rather than the name derived from it.
    body = re.sub(r"^  name: \S+$", "  name: PLACEHOLDER", text, count=1, flags=re.M)
    digest = hashlib.sha256(body.encode()).hexdigest()[:12]
    suffix = old.split(".", 1)[1] if "." in old else old
    new = f"v{digest}.{suffix}"
    if new != old:
        f.write_text(text.replace(old, new))
        renames[old] = new

if export_file.exists() and renames:
    text = export_file.read_text()
    for old, new in renames.items():
        text = text.replace(old, new)
    export_file.write_text(text)
PYEOF
}

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR"/apiresourceschema-*.yaml "$OUT_DIR/apiexport-tenancy.yaml" "$OUT_DIR/apiexport-tenancy-platform.yaml"

render tenancy-platform "${PLATFORM_CRDS[@]}"
render tenancy "${TENANT_CRDS[@]}"

echo "rendered $OUT_DIR/apiexport-tenancy{,-platform}.yaml and their schemas"
echo "the schema-less exports (tenancy-provisioner, tenancy-access) are static: $OUT_DIR/apiexport-tenancy-{provisioner,access}.yaml"
echo "these are EMBEDDED into the binary — rebuild after regenerating"
