"""
Custom Chatbot Service
======================
A lightweight conversational AI service backed by any OpenAI-compatible LLM
endpoint. Exposes a FastAPI backend with interactive Swagger docs (/docs) and
a built-in chat UI served at the root (/).

Environment variables
---------------------
LLM_ENDPOINT      Base URL of the LLM inference server (e.g. http://llm:8000)
LLM_MODEL         Model name/ID to pass in every request
LLM_API_KEY       Optional bearer token for the LLM endpoint
LLM_MAX_TOKENS    Maximum tokens per completion (default: 1024)
SYSTEM_PROMPT     Initial system instruction (overridable per session)
LOG_LEVEL         Uvicorn / app log level (default: INFO)
PORT              Port the service listens on (default: 8080)
"""

from __future__ import annotations

import logging
import os
import uuid
from typing import AsyncGenerator, Optional

import httpx
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import HTMLResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

LLM_ENDPOINT: str = os.environ.get("LLM_ENDPOINT", "http://localhost:8000")
LLM_MODEL: str = os.environ.get("LLM_MODEL", "granite-3.3-8b-instruct")
LLM_API_KEY: Optional[str] = os.environ.get("LLM_API_KEY")
LLM_MAX_TOKENS: int = int(os.environ.get("LLM_MAX_TOKENS", "1024"))
DEFAULT_SYSTEM_PROMPT: str = os.environ.get(
    "SYSTEM_PROMPT",
    "You are a helpful, concise, and friendly AI assistant. "
    "Engage naturally across multiple conversation turns and "
    "reference prior context when it is relevant.",
)
LOG_LEVEL: str = os.environ.get("LOG_LEVEL", "INFO").upper()
PORT: int = int(os.environ.get("PORT", "8080"))

logging.basicConfig(level=getattr(logging, LOG_LEVEL, logging.INFO))
logger = logging.getLogger("custom-chatbot")

# ---------------------------------------------------------------------------
# In-memory session store  { session_id: {"messages": [...], "system": str} }
# ---------------------------------------------------------------------------

_sessions: dict[str, dict] = {}

# ---------------------------------------------------------------------------
# FastAPI app
# ---------------------------------------------------------------------------

app = FastAPI(
    title="Custom Chatbot Service",
    description=(
        "A lightweight conversational AI service powered by an "
        "OpenAI-compatible LLM endpoint. "
        "Supports multi-turn conversations with per-session history, "
        "streaming responses, and a built-in chat UI."
    ),
    version="1.0.0",
    docs_url="/docs",
    redoc_url="/redoc",
)

# Serve static files (UI) from /static — mounted after routes so /docs wins
_static_dir = os.path.join(os.path.dirname(__file__), "static")


# ---------------------------------------------------------------------------
# Pydantic schemas
# ---------------------------------------------------------------------------


class ChatRequest(BaseModel):
    message: str = Field(..., description="User message to send to the assistant", min_length=1)
    session_id: Optional[str] = Field(
        None,
        description="Session ID for conversation continuity. "
        "A new session is created automatically when omitted.",
    )
    system_prompt: Optional[str] = Field(
        None,
        description="Override the default system prompt for this session. "
        "Only applied when starting a new session or when reset=True.",
    )
    stream: bool = Field(False, description="Stream the response token-by-token (SSE).")
    reset: bool = Field(
        False,
        description="Clear the conversation history before sending the message.",
    )


class ChatMessage(BaseModel):
    role: str
    content: str


class ChatResponse(BaseModel):
    session_id: str
    reply: str
    history: list[ChatMessage]


class HistoryResponse(BaseModel):
    session_id: str
    history: list[ChatMessage]


class ResetResponse(BaseModel):
    session_id: str
    message: str


class HealthResponse(BaseModel):
    status: str


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _get_or_create_session(
    session_id: Optional[str],
    system_prompt: Optional[str],
    reset: bool,
) -> tuple[str, dict]:
    """Return (session_id, session_dict), creating/resetting as needed."""
    if session_id is None or session_id not in _sessions:
        session_id = session_id or str(uuid.uuid4())
        _sessions[session_id] = {
            "messages": [],
            "system": system_prompt or DEFAULT_SYSTEM_PROMPT,
        }
    elif reset:
        _sessions[session_id] = {
            "messages": [],
            "system": system_prompt or _sessions[session_id]["system"],
        }
    return session_id, _sessions[session_id]


def _build_payload(session: dict, user_message: str) -> dict:
    messages = [{"role": "system", "content": session["system"]}]
    messages.extend(session["messages"])
    messages.append({"role": "user", "content": user_message})
    return {
        "model": LLM_MODEL,
        "messages": messages,
        "max_tokens": LLM_MAX_TOKENS,
        "temperature": 0.7,
    }


def _llm_headers() -> dict:
    headers = {"Content-Type": "application/json"}
    if LLM_API_KEY:
        headers["Authorization"] = f"Bearer {LLM_API_KEY}"
    return headers


async def _call_llm(payload: dict) -> str:
    url = f"{LLM_ENDPOINT.rstrip('/')}/v1/chat/completions"
    async with httpx.AsyncClient(timeout=120) as client:
        resp = await client.post(url, json=payload, headers=_llm_headers())
    if resp.status_code != 200:
        logger.error("LLM error %s: %s", resp.status_code, resp.text)
        raise HTTPException(status_code=502, detail=f"LLM returned {resp.status_code}")
    data = resp.json()
    return data["choices"][0]["message"]["content"]


async def _stream_llm(payload: dict) -> AsyncGenerator[str, None]:
    url = f"{LLM_ENDPOINT.rstrip('/')}/v1/chat/completions"
    payload = {**payload, "stream": True}
    async with httpx.AsyncClient(timeout=120) as client:
        async with client.stream("POST", url, json=payload, headers=_llm_headers()) as resp:
            if resp.status_code != 200:
                yield f"data: [ERROR] LLM returned {resp.status_code}\n\n"
                return
            async for line in resp.aiter_lines():
                if line.startswith("data:"):
                    chunk = line[5:].strip()
                    if chunk == "[DONE]":
                        yield "data: [DONE]\n\n"
                        return
                    yield f"data: {chunk}\n\n"


# ---------------------------------------------------------------------------
# Routes
# ---------------------------------------------------------------------------


@app.get("/health", response_model=HealthResponse, tags=["System"])
async def health() -> HealthResponse:
    """Liveness probe — always returns 200 OK when the service is up."""
    return HealthResponse(status="ok")


@app.post("/chat", response_model=ChatResponse, tags=["Chat"])
async def chat(req: ChatRequest):
    """
    Send a message and receive a reply.

    - Creates a new session automatically when `session_id` is omitted.
    - Maintains full conversation history per session (in memory).
    - Set `stream=true` to receive a Server-Sent Events stream instead.
    - Set `reset=true` to clear history before sending the message.
    """
    session_id, session = _get_or_create_session(req.session_id, req.system_prompt, req.reset)

    if req.stream:
        payload = _build_payload(session, req.message)
        # Append user turn optimistically; assistant turn accumulated client-side
        session["messages"].append({"role": "user", "content": req.message})

        return StreamingResponse(
            _stream_llm(payload),
            media_type="text/event-stream",
            headers={
                "X-Session-Id": session_id,
                "Cache-Control": "no-cache",
            },
        )

    reply = await _call_llm(_build_payload(session, req.message))
    session["messages"].append({"role": "user", "content": req.message})
    session["messages"].append({"role": "assistant", "content": reply})

    return ChatResponse(
        session_id=session_id,
        reply=reply,
        history=[ChatMessage(**m) for m in session["messages"]],
    )


@app.get("/chat/history/{session_id}", response_model=HistoryResponse, tags=["Chat"])
async def get_history(session_id: str) -> HistoryResponse:
    """Retrieve the full conversation history for a session."""
    if session_id not in _sessions:
        raise HTTPException(status_code=404, detail="Session not found")
    session = _sessions[session_id]
    return HistoryResponse(
        session_id=session_id,
        history=[ChatMessage(**m) for m in session["messages"]],
    )


@app.delete("/chat/history/{session_id}", response_model=ResetResponse, tags=["Chat"])
async def reset_session(session_id: str) -> ResetResponse:
    """Clear the conversation history for a session (keeps system prompt)."""
    if session_id not in _sessions:
        raise HTTPException(status_code=404, detail="Session not found")
    _sessions[session_id]["messages"] = []
    return ResetResponse(session_id=session_id, message="History cleared.")


# ---------------------------------------------------------------------------
# UI — serve index.html at root; all other static assets from /static
# ---------------------------------------------------------------------------


@app.get("/", response_class=HTMLResponse, include_in_schema=False)
async def ui(request: Request):
    index = os.path.join(_static_dir, "index.html")
    with open(index, encoding="utf-8") as fh:
        return HTMLResponse(content=fh.read())


app.mount("/static", StaticFiles(directory=_static_dir), name="static")
