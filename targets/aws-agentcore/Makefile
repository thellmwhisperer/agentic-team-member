SHELL := /bin/sh

PYTHON ?= $(shell command -v python3.14 2>/dev/null || command -v python3.13 2>/dev/null || command -v python3.12 2>/dev/null || command -v python3)
PYTEST ?= $(PYTHON) -m pytest
CDK ?= cdk
CDK_APP ?= $(PYTHON) infrastructure/app.py
CDK_OUT ?= ../../.atm/cdk.out
DOCKER ?= docker
IMAGE_NAME ?= atm-aws-agentcore
IMAGE_TAG ?= local
BUILD_CONTEXT ?= ../..
TARGET_PYTHONPATH ?= src:.:../..

.PHONY: help test smoke cdk-synth docker-build docker-smoke

help:
	@printf '%s\n' \
		'Targets:' \
		'  test          Run AWS target adapter tests locally' \
		'  smoke         Run import/entrypoint smoke without AWS' \
		'  cdk-synth     Synthesize the AWS AgentCore CDK stack; does not deploy' \
		'  docker-build  Build the AgentCore image from the monorepo root' \
		'  docker-smoke  Build then run the image smoke test'

test:
	PYTHONPATH="$(TARGET_PYTHONPATH)" $(PYTEST) -p no:cacheprovider tests

smoke:
	PYTHONPATH="$(TARGET_PYTHONPATH)" $(PYTHON) scripts/smoke-runtime.py

cdk-synth:
	PYTHONPATH="infrastructure:$(TARGET_PYTHONPATH)" $(CDK) synth -a "$(CDK_APP)" -o "$(CDK_OUT)"

docker-build:
	$(DOCKER) build -f Dockerfile -t $(IMAGE_NAME):$(IMAGE_TAG) $(BUILD_CONTEXT)

docker-smoke: docker-build
	$(DOCKER) run --rm \
		-e ATM_ENABLE_BEDROCK_PROXY=false \
		-e ATM_ENABLE_GIT_HTTPS_AUTH=false \
		$(IMAGE_NAME):$(IMAGE_TAG) \
		python /app/targets/aws-agentcore/scripts/smoke-runtime.py
