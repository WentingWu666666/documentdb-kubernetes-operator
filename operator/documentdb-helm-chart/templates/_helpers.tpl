{{- define "documentdb-chart.name" -}}
documentdb-operator
{{- end -}}

{{/*
documentdb.imageRef composes a container image reference from a registry prefix,
a repository, and an optional tag, following the canonical Docker/containerd
reference rule for deciding whether the repository already carries a registry
host: the first path segment is treated as a registry host when it contains a
"." or ":" (port), or equals "localhost". In that case the repository is used
verbatim and the registry prefix is ignored (this is also the per-component
per-registry override path). Otherwise the registry prefix is prepended.

The repository must be a host/path only. If it already carries a tag or digest
(a ":" or "@" in the final path segment) the template fails, so the tag is
always supplied via the component's tag field (or documentDbVersion for the
data-plane images) rather than embedded in the repository.

Usage:
  {{ include "documentdb.imageRef" (dict "registry" .Values.image.registry "repo" $repo "tag" $tag) }}
Pass tag "" to compose a bare repository (host+path, no tag).
*/}}
{{- define "documentdb.imageRef" -}}
{{- $lastSeg := (splitList "/" .repo) | last -}}
{{- if or (contains ":" $lastSeg) (contains "@" $lastSeg) -}}
{{- fail (printf "image repository %q must be a host/path without a tag or digest; set the tag via the component's tag field (or documentDbVersion for data-plane images)" .repo) -}}
{{- end -}}
{{- $first := (splitList "/" .repo) | first -}}
{{- $full := .repo -}}
{{- if not (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $full = printf "%s/%s" .registry .repo -}}
{{- end -}}
{{- if .tag -}}
{{- printf "%s:%s" $full .tag -}}
{{- else -}}
{{- $full -}}
{{- end -}}
{{- end -}}