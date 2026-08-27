Day N:

{{- if eq .UI_STATUS "running" }}

- {{ .SERVICE_NAME }} chat UI is available at https://{{ .UI_ROUTE }}.
- {{ .SERVICE_NAME }} API and docs are available at https://{{ .API_ROUTE }}/docs.
{{- else }}

- {{ .SERVICE_NAME }} is unavailable. Please make sure the 'custom-chatbot' deployment is running.
{{- end }}
