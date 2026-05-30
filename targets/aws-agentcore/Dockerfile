# syntax=docker/dockerfile:1.7
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    ATM_PYTHON=python \
    ATM_HARNESS_MODULE=agentic_tdd_runner.agent \
    ATM_SOURCE_REPO=/app \
    ATM_BASE_CONFIG=/app/config/agent.toml \
    PYTHONPATH=/app:/app/targets/aws-agentcore:/app/targets/aws-agentcore/src

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        gh \
        git \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

COPY targets/aws-agentcore/requirements.txt /tmp/atm-aws-agentcore-requirements.txt
RUN python -m pip install --upgrade pip \
    && python -m pip install \
        'requests>=2.31,<3' \
        -r /tmp/atm-aws-agentcore-requirements.txt

COPY agentic_tdd_runner ./agentic_tdd_runner
COPY config ./config
COPY targets/aws-agentcore/runtime ./targets/aws-agentcore/runtime
COPY targets/aws-agentcore/src ./targets/aws-agentcore/src
COPY targets/aws-agentcore/scripts ./targets/aws-agentcore/scripts
COPY targets/aws-agentcore/pyproject.toml ./targets/aws-agentcore/pyproject.toml

CMD ["python", "-m", "runtime.atm_agent"]
