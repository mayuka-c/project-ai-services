IMAGE=custom-chatbot-service
TAG?=v1.0.0

run:
	PORT=$${PORT:-8080} /var/venv/bin/python -m uvicorn main:app --host 0.0.0.0 --port $$PORT
.PHONY: run
