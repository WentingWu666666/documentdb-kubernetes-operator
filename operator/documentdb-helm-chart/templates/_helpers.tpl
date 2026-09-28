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

The repository must be a non-empty host/path only. Rendering fails (naming the
offending setting via "name") when the repository is empty, when it already
carries a tag or digest (a ":" or "@" in the final path segment), or when the
registry prefix would be needed but is empty. The tag is always supplied via the
component's tag field (or documentDbVersion for the data-plane images) rather
than embedded in the repository.

Usage:
  {{ include "documentdb.imageRef" (dict "name" "image.foo.repository" "registry" .Values.image.registry "repo" $repo "tag" $tag) }}
Pass tag "" to compose a bare repository (host+path, no tag).
*/}}
{{- define "documentdb.imageRef" -}}
{{- $name := .name | default "image repository" -}}
{{- if not .repo -}}
{{- fail (printf "%s is empty; set it to a host/path (with image.registry) or a full host reference" $name) -}}
{{- end -}}
{{- $lastSeg := (splitList "/" .repo) | last -}}
{{- if or (contains ":" $lastSeg) (contains "@" $lastSeg) -}}
{{- fail (printf "%s value %q must be a host/path without a tag or digest; set the tag via the component's tag field (or documentDbVersion for data-plane images)" $name .repo) -}}
{{- end -}}
{{- $first := (splitList "/" .repo) | first -}}
{{- $full := .repo -}}
{{- if not (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- if not .registry -}}
{{- fail (printf "image.registry is empty but %s (%q) is a relative path; set image.registry or use a full host reference in the repository" $name .repo) -}}
{{- end -}}
{{- $full = printf "%s/%s" .registry .repo -}}
{{- end -}}
{{- if .tag -}}
{{- printf "%s:%s" $full .tag -}}
{{- else -}}
{{- $full -}}
{{- end -}}
{{- end -}}

{{/*
documentdb.pullPolicy validates and echoes an image pull policy. An empty value
yields an empty string (the caller omits the setting and the consumer applies
its own default); a non-empty value must be one of Always, IfNotPresent, or
Never, otherwise rendering fails naming the offending setting via "name".

Usage:
  {{ include "documentdb.pullPolicy" (dict "name" "image.gateway.pullPolicy" "policy" .Values.image.gateway.pullPolicy) }}
*/}}
{{- define "documentdb.pullPolicy" -}}
{{- if .policy -}}
{{- if not (has .policy (list "Always" "IfNotPresent" "Never")) -}}
{{- fail (printf "%s must be one of Always, IfNotPresent, Never (got %q)" .name .policy) -}}
{{- end -}}
{{- .policy -}}
{{- end -}}
{{- end -}}