Day N:

{{- if eq .UI_STATUS "running" }}

- {{ .SERVICE_NAME }} chat UI is available at {{ .UI_URL }}.
- {{ .SERVICE_NAME }} API docs are available at {{ .UI_URL }}/docs.
{{- else }}

- {{ .SERVICE_NAME }} is unavailable. Please make sure the 'custom-chatbot' pod is running.
{{- end }}
