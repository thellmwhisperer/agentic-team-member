from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from aws_cdk import (
    CfnOutput,
    Duration,
    RemovalPolicy,
    Stack,
    aws_bedrockagentcore as bedrockagentcore,
    aws_ecr_assets as ecr_assets,
    aws_iam as iam,
    aws_lambda as lambda_,
    aws_logs as logs,
    aws_secretsmanager as secretsmanager,
)
from constructs import Construct


@dataclass(frozen=True)
class TargetSettings:
    stack_name: str
    runtime_name: str
    gateway_name: str
    runner_model_id: str
    branch_prefix: str
    github_token_secret_name: str
    roca_token_secret_name: str
    roca_mcp_url: str
    github_repo_allowlist: str
    permission_driven: bool


def target_context(node: Any) -> TargetSettings:
    return TargetSettings(
        stack_name=_context_value(node, "atmStackName", "ATM_AGENTCORE_STACK_NAME", "AtmAgentCoreStack"),
        runtime_name=_context_value(node, "atmRuntimeName", "ATM_AGENTCORE_RUNTIME_NAME", "atm-agentcore-runner"),
        gateway_name=_context_value(node, "atmGatewayName", "ATM_AGENTCORE_GATEWAY_NAME", "atm-agentcore-github"),
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
        github_repo_allowlist=_context_value(node, "githubRepoAllowlist", "GITHUB_REPO_ALLOWLIST", ""),
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

        target_root = Path(__file__).resolve().parents[1]
        repo_root = Path(__file__).resolve().parents[3]
        tool_schema = _load_gateway_tool_schema(
            target_root / "infrastructure" / "gateway" / "github-tools.json"
        )

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

        github_fn = lambda_.Function(
            self,
            "GithubToolFunction",
            runtime=lambda_.Runtime.PYTHON_3_12,
            architecture=lambda_.Architecture.ARM_64,
            handler="atm_cloud.github_gateway.lambda_handler",
            code=lambda_.Code.from_asset(str(target_root / "src")),
            timeout=Duration.seconds(30),
            memory_size=256,
            log_group=logs.LogGroup(
                self,
                "GithubToolFunctionLogGroup",
                retention=logs.RetentionDays.THREE_DAYS,
                removal_policy=RemovalPolicy.DESTROY,
            ),
            environment={
                "GITHUB_TOKEN_SECRET_ARN": github_token_secret.secret_arn,
                "GITHUB_REPO_ALLOWLIST": settings.github_repo_allowlist,
                "ATM_BRANCH_PREFIX": settings.branch_prefix,
            },
        )
        github_token_secret.grant_read(github_fn)

        runtime_image = ecr_assets.DockerImageAsset(
            self,
            "AtmRuntimeImage",
            directory=str(repo_root),
            exclude=[
                ".git",
                ".atm",
                ".worktree",
                ".worktrees",
                "cdk.out",
                "targets/aws-agentcore/cdk.out",
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
            "GITHUB_REPO_ALLOWLIST": settings.github_repo_allowlist,
            "ATM_PYTHON": "python",
            "ATM_HARNESS_MODULE": "agentic_tdd_runner.agent",
            "ATM_BRANCH_PREFIX": settings.branch_prefix,
            "ATM_ENABLE_BEDROCK_PROXY": "true",
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

        gateway_role = iam.Role(
            self,
            "GithubGatewayRole",
            assumed_by=iam.ServicePrincipal("bedrock-agentcore.amazonaws.com"),
            inline_policies={
                "GithubGatewayPolicy": iam.PolicyDocument(
                    statements=[
                        iam.PolicyStatement(
                            sid="InvokeGithubToolLambda",
                            actions=["lambda:InvokeFunction"],
                            resources=[github_fn.function_arn],
                        )
                    ]
                )
            },
        )

        gateway = bedrockagentcore.CfnGateway(
            self,
            "GithubGateway",
            name=settings.gateway_name,
            description="Governed GitHub tool perimeter for ATM AWS AgentCore",
            role_arn=gateway_role.role_arn,
            protocol_type="MCP",
            authorizer_type="AWS_IAM",
            protocol_configuration=bedrockagentcore.CfnGateway.GatewayProtocolConfigurationProperty(
                mcp=bedrockagentcore.CfnGateway.MCPGatewayConfigurationProperty(
                    instructions=(
                        "Use these tools for ATM GitHub side effects. "
                        "Prefer narrow operations: read issues, create ATM branches, "
                        "commit files, open PRs, and comment on issues."
                    ),
                    supported_versions=["2025-06-18"],
                    search_type="SEMANTIC",
                )
            ),
        )

        target = bedrockagentcore.CfnGatewayTarget(
            self,
            "GithubGatewayTarget",
            gateway_identifier=gateway.attr_gateway_identifier,
            name="github-tools",
            description="Allowlisted GitHub tools implemented by Lambda",
            target_configuration=bedrockagentcore.CfnGatewayTarget.TargetConfigurationProperty(
                mcp=bedrockagentcore.CfnGatewayTarget.McpTargetConfigurationProperty(
                    lambda_=bedrockagentcore.CfnGatewayTarget.McpLambdaTargetConfigurationProperty(
                        lambda_arn=github_fn.function_arn,
                        tool_schema=bedrockagentcore.CfnGatewayTarget.ToolSchemaProperty(
                            inline_payload=tool_schema,
                        ),
                    )
                )
            ),
            credential_provider_configurations=[
                bedrockagentcore.CfnGatewayTarget.CredentialProviderConfigurationProperty(
                    credential_provider_type="GATEWAY_IAM_ROLE",
                )
            ],
        )
        target.node.add_dependency(gateway)

        CfnOutput(self, "AtmRuntimeArn", value=runtime.attr_agent_runtime_arn)
        CfnOutput(self, "AtmRuntimeId", value=runtime.attr_agent_runtime_id)
        CfnOutput(self, "GithubGatewayId", value=gateway.attr_gateway_identifier)
        CfnOutput(self, "GithubGatewayUrl", value=gateway.attr_gateway_url)
        CfnOutput(self, "GithubGatewayTargetId", value=target.attr_target_id)
        CfnOutput(self, "GithubTokenSecretName", value=settings.github_token_secret_name)
        if settings.roca_token_secret_name:
            CfnOutput(self, "RocaTokenSecretName", value=settings.roca_token_secret_name)


def _load_gateway_tool_schema(path: Path) -> list[Any]:
    tools = json.loads(path.read_text())
    return [
        bedrockagentcore.CfnGatewayTarget.ToolDefinitionProperty(
            name=tool["name"],
            description=tool["description"],
            input_schema=tool["inputSchema"],
        )
        for tool in tools
    ]


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
