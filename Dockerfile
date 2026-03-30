FROM python:3.14-slim

WORKDIR /app

# Install uv for fast dependency management
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

# Copy project files
COPY pyproject.toml .
COPY src/ src/

# Install dependencies (no venv needed in container)
RUN uv pip install --system .

EXPOSE 8000

CMD ["python", "-m", "uvicorn", "bifrost.spike_oauth:app", "--host", "0.0.0.0", "--port", "8000"]
