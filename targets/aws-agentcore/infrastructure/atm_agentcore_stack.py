from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from aws_cdk import (
    CfnOutput,
    Stack,
    aws_bedrockagentcore as bedrockagentcore,
    aws_ecr_assets as ecr_assets,
    aws_iam as iam,
    aws_secretsmanager as secretsmanager,
)
from constructs import Construct


@dataclass(frozen=True)
class TargetSettings:
    stack_name: str
    runtime_name: str
    runner_model_id: str
    branch_prefix: str
    github_token_secret_name: str
    roca_token_secret_name: str
    roca_mcp_url: str
    npm_scope: str
    permission_driven: bool


def target_context(node: Any) -> TargetSettings:
    return TargetSettings(
        stack_name=_context_value(node, "atmStackName", "ATM_AGENTCORE_STACK_NAME", "AtmAgentCoreStack"),
        runtime_name=_context_value(node, "atmRuntimeName", "ATM_AGENTCORE_RUNTIME_NAME", "atm-agentcore-runner"),
        runner_model_id=_context_value(node, "atmRunnerModelId", "ATM_BEDROCK_MODEL_ID", ""),
        branch_prefix=_context_value(node, "atmBranchPrefix", "ATM_BRANCH_PREFIX", "atm-agentcore/"),
        github_token_secret_name=_context_value(
            node,
            "githubTokenSecretName",
            "GITHUB_TOKEN_SECRET_NAME",
            "atm-agentcore/github-token",
        ),
        roca_token_secret_name=_context_value(node, "rocaTokenSecretName", "ROCA_TOKEN_SECRET_NAME", ""),
        roca_mcp_url=_context_value(node, "rocaMcpUrl", "ROCA_CLOUD_MCP_URL", ""),
        npm_scope=_context_value(node, "npmScope", "ATM_NPM_SCOPE", ""),
        permission_driven=_context_bool(
            os.environ.get("ATM_PERMISSION_DRIVEN")
            if os.environ.get("ATM_PERMISSION_DRIVEN") is not None
            else node.try_get_context("atmPermissionDriven"),
            default=False,
        ),
    )


class AtmAgentCoreStack(Stack):
    def __init__(
        self,
        scope: Construct,
        construct_id: str,
        *,
        settings: TargetSettings,
        **kwargs: Any,
    ):
        super().__init__(scope, construct_id, **kwargs)

        repo_root = Path(__file__).resolve().parents[3]

        github_token_secret = secretsmanager.Secret.from_secret_name_v2(
            self,
            "GithubTokenSecret",
            settings.github_token_secret_name,
        )
        roca_token_secret = (
            secretsmanager.Secret.from_secret_name_v2(
                self,
                "RocaTokenSecret",
                settings.roca_token_secret_name,
            )
            if settings.roca_token_secret_name
            else None
        )

        runtime_image = ecr_assets.DockerImageAsset(
            self,
            "AtmRuntimeImage",
            directory=str(repo_root),
            exclude=[
                ".git",
                ".atm",
                ".tmp",
                ".workspace",
                ".worktree",
                ".worktrees",
                "cdk.out",
                "targets/aws-agentcore/cdk.out",
                "targets/aws-agentcore/.venv",
                "**/.venv",
                "**/__pycache__",
                "**/*.pyc",
                "**/*.local.toml",
                "**/.env.local",
                "**/.env.*.local",
            ],
            file="targets/aws-agentcore/Dockerfile",
            platform=ecr_assets.Platform.LINUX_ARM64,
        )

        runtime_role = iam.Role(
            self,
            "AtmRuntimeRole",
            assumed_by=iam.ServicePrincipal("bedrock-agentcore.amazonaws.com"),
            inline_policies={
                "AtmRuntimePolicy": iam.PolicyDocument(
                    statements=[
                        iam.PolicyStatement(
                            sid="EcrToken",
                            actions=["ecr:GetAuthorizationToken"],
                            resources=["*"],
                        ),
                        iam.PolicyStatement(
                            sid="EcrPull",
                            actions=[
                                "ecr:BatchCheckLayerAvailability",
                                "ecr:BatchGetImage",
                                "ecr:GetDownloadUrlForLayer",
                            ],
                            resources=[runtime_image.repository.repository_arn],
                        ),
                        iam.PolicyStatement(
                            sid="RuntimeLogs",
                            actions=[
                                "logs:CreateLogGroup",
                                "logs:DescribeLogGroups",
                                "logs:DescribeLogStreams",
                                "logs:CreateLogStream",
                                "logs:PutLogEvents",
                            ],
                            resources=[
                                f"arn:{self.partition}:logs:{self.region}:{self.account}:log-group:/aws/bedrock-agentcore/runtimes/*"
                            ],
                        ),
                        iam.PolicyStatement(
                            sid="Observability",
                            actions=[
                                "xray:PutTraceSegments",
                                "xray:PutTelemetryRecords",
                                "xray:GetSamplingRules",
                                "xray:GetSamplingTargets",
                                "cloudwatch:PutMetricData",
                            ],
                            resources=["*"],
                        ),
                        iam.PolicyStatement(
                            sid="InvokeBedrockModels",
                            actions=[
                                "bedrock:InvokeModel",
                                "bedrock:InvokeModelWithResponseStream",
                            ],
                            resources=[
                                f"arn:{self.partition}:bedrock:*::foundation-model/*",
                                f"arn:{self.partition}:bedrock:{self.region}:{self.account}:*",
                            ],
                        ),
                        iam.PolicyStatement(
                            sid="ReadGithubToken",
                            actions=["secretsmanager:GetSecretValue"],
                            resources=[github_token_secret.secret_arn],
                        ),
                    ]
                    + (
                        [
                            iam.PolicyStatement(
                                sid="ReadRocaToken",
                                actions=["secretsmanager:GetSecretValue"],
                                resources=[roca_token_secret.secret_arn],
                            )
                        ]
                        if roca_token_secret is not None
                        else []
                    )
                )
            },
        )

        environment_variables = {
            "GITHUB_TOKEN_SECRET_ARN": github_token_secret.secret_arn,
            "ATM_PYTHON": "python",
            "ATM_HARNESS_MODULE": "agentic_tdd_runner.agent",
            "ATM_BRANCH_PREFIX": settings.branch_prefix,
            "ATM_ENABLE_BEDROCK_PROXY": "true",
            "ATM_ENABLE_GITHUB_PACKAGES_AUTH": "true" if settings.npm_scope else "false",
            "ATM_NPM_REGISTRY": "https://npm.pkg.github.com",
            "ATM_BEDROCK_MODEL_ID": settings.runner_model_id,
            "ATM_PERMISSION_DRIVEN": "true" if settings.permission_driven else "false",
            "ATM_BASE_CONFIG": "/app/config/agent.toml",
            "ATM_CONFIG": "/tmp/atm-agentcore/agentcore-agent.toml",
            "ATM_REPO_CACHE_ROOT": "/tmp/atm-agentcore/repos",
            "ATM_LOG_DIR": "/tmp/atm-agentcore/logs",
            "ATM_STREAM_HARNESS_OUTPUT": "true",
            "AWS_REGION": self.region,
            "AWS_DEFAULT_REGION": self.region,
        }
        if settings.npm_scope:
            environment_variables["ATM_NPM_SCOPE"] = settings.npm_scope
        if settings.roca_mcp_url:
            environment_variables["ROCA_CLOUD_MCP_URL"] = settings.roca_mcp_url
        if roca_token_secret is not None:
            environment_variables["ROCA_CLOUD_API_TOKEN_SECRET_ARN"] = roca_token_secret.secret_arn

        runtime = bedrockagentcore.CfnRuntime(
            self,
            "AtmRuntime",
            agent_runtime_name=settings.runtime_name,
            agent_runtime_artifact=bedrockagentcore.CfnRuntime.AgentRuntimeArtifactProperty(
                container_configuration=bedrockagentcore.CfnRuntime.ContainerConfigurationProperty(
                    container_uri=runtime_image.image_uri,
                )
            ),
            network_configuration=bedrockagentcore.CfnRuntime.NetworkConfigurationProperty(
                network_mode="PUBLIC",
            ),
            protocol_configuration="HTTP",
            role_arn=runtime_role.role_arn,
            description="ATM runner hosted on AWS AgentCore Runtime",
            environment_variables=environment_variables,
        )

        CfnOutput(self, "AtmRuntimeArn", value=runtime.attr_agent_runtime_arn)
        CfnOutput(self, "AtmRuntimeId", value=runtime.attr_agent_runtime_id)
        CfnOutput(self, "GithubTokenSecretName", value=settings.github_token_secret_name)
        if settings.roca_token_secret_name:
            CfnOutput(self, "RocaTokenSecretName", value=settings.roca_token_secret_name)


def _context_value(node: Any, key: str, env_var: str, default: str) -> str:
    env_value = os.environ.get(env_var)
    if env_value is not None:
        return env_value
    value = node.try_get_context(key)
    if value is None:
        return default
    return str(value)


def _context_bool(value: Any, *, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    return str(value).strip().lower() in {"1", "true", "yes", "on"}
