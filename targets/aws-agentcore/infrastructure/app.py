#!/usr/bin/env python3
from __future__ import annotations

import os

import aws_cdk as cdk

from atm_agentcore_stack import AtmAgentCoreStack, target_context


app = cdk.App()
settings = target_context(app.node)

AtmAgentCoreStack(
    app,
    settings.stack_name,
    settings=settings,
    env=cdk.Environment(
        account=os.environ.get("CDK_DEFAULT_ACCOUNT"),
        region=(
            os.environ.get("CDK_DEFAULT_REGION")
            or os.environ.get("AWS_REGION")
            or os.environ.get("AWS_DEFAULT_REGION")
        ),
    ),
)

app.synth()
