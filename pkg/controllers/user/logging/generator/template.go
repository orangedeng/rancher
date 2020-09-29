package generator

var Template = `
{{define "cluster-template" }}
{{- if .IncludeRke }}
{{- template "source-rke" . -}}
{{- template "filter-rke" . -}}
{{end }}
{{- template "source-container" . -}}
{{- if .EnableExceptionStackMatch }}
{{- template "match-detect-exceptions" . -}}
{{end}}
{{- template "filter-container" . -}}
{{- template "filter-add-logtype" . -}}
{{- if and .EnableMultiLineFilter (or .MultiLineStartRegexp .MultiLineEndRegexp) }}
{{- template "filter-concat" . -}}
{{end}}
{{- template "filter-custom-tags" . -}}
{{- template "filter-prometheus" . -}}
{{- template "filter-exclude-system-component" . -}}
{{- template "filter-sumo" . -}}
{{- template "filter-json" . -}}
{{- template "match" . -}}
{{end}}

{{define "project-template" }}
{{ range $i, $store := . }}
{{- if $store.IncludeRke }}
{{- template "source-rke" $store -}}
{{- template "filter-rke" $store -}}
{{end }}
{{- template "source-project-container" $store -}}
{{- if $store.EnableExceptionStackMatch }}
{{- template "match-detect-exceptions" $store -}}
{{end}}
{{- template "filter-container" $store -}}
{{- template "filter-add-projectid" $store -}}
{{- if and $store.EnableMultiLineFilter (or $store.MultiLineStartRegexp $store.MultiLineEndRegexp) }}
{{- template "filter-concat" $store -}}
{{end}}
{{- template "filter-custom-tags" $store -}}
{{- template "filter-prometheus" $store -}}
{{- template "filter-sumo" $store -}}
{{- template "filter-json" $store -}}
{{- template "match" $store -}}
{{end}}
{{end}}
`
