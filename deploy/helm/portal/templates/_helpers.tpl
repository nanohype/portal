{{/*
A container image reference, with the chart's appVersion as the tag when the
values do not pin one.

Every process in this chart is built from this repo at one version, so the tag
they should share is the chart's own appVersion. Writing `latest` into the
defaults instead meant the four deployments could silently resolve to four
different builds, and that an install pinned nothing at all — `latest` moves.

Called as: include "portal.image" (dict "img" .Values.image.server "ctx" $)
*/}}
{{- define "portal.image" -}}
{{- $tag := .img.tag | default .ctx.Chart.AppVersion -}}
{{- printf "%s:%s" .img.repository $tag -}}
{{- end -}}

{{/*
The executor image: the tofu/terragrunt runner the Kubernetes executor starts a
pod from per run. Same appVersion default as the four long-running images, and
for the same reason — it is built by the same release.
*/}}
{{- define "portal.executorImage" -}}
{{- .Values.config.executorImage | default (printf "%s:%s" (include "portal.executorRepository" .) .Chart.AppVersion) -}}
{{- end -}}

{{/*
The executor repository, used two ways: as the base for executorImage above,
and on its own as EXECUTOR_IMAGE_PREFIX, which the worker appends
`:tofu-<version>` to whenever a workspace pins a tofu version.

The chart set neither, so the worker fell back to its Go default of
`portal-executor` — an unqualified name, i.e. docker.io/library/portal-executor.
A workspace with a pinned tofu version therefore reached for a Docker Hub image
nobody publishes, on a code path only that one setting takes, which is why an
otherwise healthy portal would fail on exactly those runs.
*/}}
{{- define "portal.executorRepository" -}}
{{- .Values.config.executorImagePrefix | default "ghcr.io/nanohype/portal/executor" -}}
{{- end -}}

{{/*
Whether the worker needs its own ServiceAccount.

One definition, because there are two places that have to agree: rbac.yaml
decides whether to create the account and its grants, and worker-deployment.yaml
decides whether to run the pod as it. A worker that gets the grants but not the
account runs as `default` and every in-cluster call it makes is denied — with
nothing in the render to show for it, since both files are individually valid.
*/}}
{{- define "portal.worker.needsServiceAccount" -}}
{{- if or .Values.argocdSync.enabled
        .Values.clusterWatchback.enabled
        .Values.clusterHealth.enabled
        .Values.unwedge.enabled
        (eq .Values.config.executorType "kubernetes") -}}
true
{{- end -}}
{{- end -}}

{{/*
The ServiceAccount every portal pod runs under.

serviceAccount.name names an account this chart does not create — under the
eks-agent-platform operator that is `tenant-runtime`, which the operator binds
to the tenant IAM role with a Pod Identity association. The association targets
exactly system:serviceaccount:<namespace>:tenant-runtime, so a pod running as
anything else is a pod with no AWS identity. That failure is silent in the way
this org keeps meeting: the release deploys, the pods run, and the object store
is simply never reachable.

Falls back to the chart's own worker account, which is what a standalone or
local install wants.
*/}}
{{- define "portal.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (printf "%s-worker" .Release.Name) -}}
{{- end -}}

{{/*
Whether THIS CHART creates the ServiceAccount.

Naming an external account and creating one are mutually exclusive: creating
`tenant-runtime` here would fight the operator's own CreateOrUpdate over the
same object, and adopting it into the Helm release makes an uninstall delete an
identity the operator owns.
*/}}
{{- define "portal.createServiceAccount" -}}
{{- if and (include "portal.worker.needsServiceAccount" .) (not .Values.serviceAccount.name) -}}
true
{{- end -}}
{{- end -}}
