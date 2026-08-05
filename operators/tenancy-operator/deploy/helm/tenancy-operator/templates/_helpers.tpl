{{/*
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
*/}}

{{- define "tenancy-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "tenancy-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "tenancy-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "tenancy-operator.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "tenancy-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tenancy-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "tenancy-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "tenancy-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "tenancy-operator.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- end -}}

{{/*
The operator's flags.

Kept in one place because several of them are only correct in relation to
something outside this chart — the paths must match the tree the installer built,
the OIDC settings must match kcp's authenticator, and the bind addresses must
match the ports the Deployment probes. Scattering them across the template is how
those pairs drift apart.

Optional path overrides are omitted when empty so the operator derives them from
--paths-root, rather than being handed an empty string.
*/}}
{{- define "tenancy-operator.pathArgs" }}
- --paths-root={{ .Values.paths.root }}
{{- with .Values.paths.tenantFleetRoot }}
- --paths-tenant-fleet-root={{ . }}
{{- end }}
{{- with .Values.paths.directory }}
- --paths-directory={{ . }}
{{- end }}
{{- with .Values.paths.exports }}
- --paths-exports={{ . }}
{{- end }}
{{- with .Values.paths.providers }}
- --paths-providers={{ . }}
{{- end }}
{{- with .Values.paths.virtualWorkspacePrefix }}
- --paths-virtual-workspace-prefix={{ . }}
{{- end }}
{{- with .Values.kcp.server }}
- --kcp-server={{ . }}
{{- end }}
{{- with .Values.kcp.serverName }}
- --kcp-server-name={{ . }}
{{- end }}
{{- end -}}

{{/*
The installer's flags. It reads the SAME paths as the operator — the installer
building one tree while the operator watches another is the failure this sharing
is designed to make impossible.
*/}}
{{- define "tenancy-operator.initArgs" -}}
- init
{{- include "tenancy-operator.pathArgs" . }}
- --workspace-ready-timeout={{ .Values.init.workspaceReadyTimeout }}
- --log-level={{ .Values.log.level }}
{{- end -}}

{{- define "tenancy-operator.args" -}}
- operator
{{- include "tenancy-operator.pathArgs" . }}
- --kcp-platform-endpoint-slice={{ .Values.kcp.endpointSlices.platform }}
- --kcp-tenancy-endpoint-slice={{ .Values.kcp.endpointSlices.tenancy }}
- --kcp-provisioner-endpoint-slice={{ .Values.kcp.endpointSlices.provisioner }}
- --kcp-access-endpoint-slice={{ .Values.kcp.endpointSlices.access }}
- --oidc-username-claim={{ .Values.oidc.usernameClaim }}
- "--oidc-username-prefix={{ .Values.oidc.usernamePrefix }}"
- --tenancy-personal-tenants-enabled={{ .Values.tenancy.personalTenantsEnabled }}
- --tenancy-tenant-quota={{ .Values.tenancy.tenantQuota }}
- --tenancy-project-quota={{ .Values.tenancy.projectQuota }}
- --tenancy-naming-strategy={{ .Values.tenancy.namingStrategy }}
- --tenancy-tenant-workspace-type={{ .Values.tenancy.tenantWorkspaceType }}
- --tenancy-project-workspace-type={{ .Values.tenancy.projectWorkspaceType }}
- --reconcilers-seed-tenant-enabled={{ .Values.reconcilers.seedTenant }}
- --reconcilers-seed-project-enabled={{ .Values.reconcilers.seedProject }}
- --reconcilers-tenant-workspace-enabled={{ .Values.reconcilers.tenantWorkspace }}
- --reconcilers-owner-membership-enabled={{ .Values.reconcilers.ownerMembership }}
- --reconcilers-membership-rbac-enabled={{ .Values.reconcilers.membershipRBAC }}
- --reconcilers-project-workspace-enabled={{ .Values.reconcilers.projectWorkspace }}
- --reconcilers-index-enabled={{ .Values.reconcilers.index }}
- --controllers-user-enabled={{ .Values.controllers.user }}
- --controllers-tenant-enabled={{ .Values.controllers.tenant }}
- --controllers-project-enabled={{ .Values.controllers.project }}
- --controllers-workspace-enabled={{ .Values.controllers.workspace }}
- --controllers-membership-enabled={{ .Values.controllers.membership }}
- --leader-elect={{ .Values.leaderElect }}
- --log-level={{ .Values.log.level }}
- --metrics-bind-address=:{{ .Values.metrics.port }}
- --health-probe-bind-address=:{{ .Values.health.port }}
{{- with .Values.extraArgs }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
The installer's container, shared by BOTH init modes.

Defined once so the initContainer and the Job cannot drift into running the same
installer with different flags, image or credential — a drift whose symptom would
be a Job reporting success while the pod-local install did something else.
*/}}
{{- define "tenancy-operator.initContainer" -}}
- name: init
  image: {{ include "tenancy-operator.image" . }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  # args only (no command) so the image ENTRYPOINT is used — /operator/manager in
  # the production image, /entrypoint in the Tilt hot-reload image.
  args:
    {{- include "tenancy-operator.initArgs" . | nindent 4 }}
  env:
  - name: KUBECONFIG
    value: /etc/kcp-admin/kubeconfig
  volumeMounts:
  - name: kcp-admin
    mountPath: /etc/kcp-admin
    readOnly: true
  {{- with .Values.securityContext }}
  securityContext:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}

{{/* The admin credential the installer mounts. Shared for the same reason. */}}
{{- define "tenancy-operator.initVolume" -}}
- name: kcp-admin
  secret:
    secretName: {{ .Values.init.kcpAdminSecretName }}
    items:
    - key: {{ .Values.init.kcpAdminSecretKey }}
      path: kubeconfig
    defaultMode: 420
{{- end -}}

{{/*
The virtual workspace's flags.

Shares pathArgs with the other two commands — the VW resolves the directory
workspace from the same layout the installer built and the manager watches, so a
disagreement would have it serving Users into a tree nobody reconciles.
*/}}
{{- define "tenancy-operator.vwArgs" -}}
- virtual-workspace
{{- include "tenancy-operator.pathArgs" . }}
- --oidc-issuer-url={{ .Values.virtualWorkspace.oidc.issuerURL }}
- --oidc-client-id={{ .Values.virtualWorkspace.oidc.clientID }}
{{- if .Values.virtualWorkspace.oidc.caSecretName }}
- --oidc-ca-file=/etc/oidc/{{ .Values.virtualWorkspace.oidc.caSecretKey }}
{{- end }}
- --oidc-username-claim={{ .Values.oidc.usernameClaim }}
- "--oidc-username-prefix={{ .Values.oidc.usernamePrefix }}"
- "--virtual-workspace-bind-address={{ .Values.virtualWorkspace.bindAddress }}"
- --virtual-workspace-tls-cert-file=/etc/vw-tls/tls.crt
- --virtual-workspace-tls-private-key-file=/etc/vw-tls/tls.key
- --log-level={{ .Values.log.level }}
{{- end -}}

{{- define "tenancy-operator.vwFullname" -}}
{{- printf "%s-vw" (include "tenancy-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "tenancy-operator.vwSelectorLabels" -}}
{{ include "tenancy-operator.selectorLabels" . }}
app.kubernetes.io/component: virtual-workspace
{{- end -}}
