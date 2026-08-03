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
