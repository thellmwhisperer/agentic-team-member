# AWS AgentCore Target

This directory is reserved for the official AWS AgentCore execution target.

The target will host the same `agentic_tdd_runner` harness in AWS, using a
target-local runtime adapter, Docker image, CDK infrastructure, Bedrock model
proxy, and optional cloud integrations.

Migration rules:

- Do not vendor `agentic_tdd_runner`.
- Build Docker images from the monorepo root so the image can copy or install
  the canonical harness.
- Keep AWS, CDK, boto3, AgentCore, CloudWatch, and AWS secret handling under
  this target.
- Keep Roca or other durable memory integrations optional.
- Use placeholders and documented environment variables for AWS profile,
  region, model id, GitHub token secret, Roca token secret, repo allowlist, and
  stack name.

The existing standalone `atm-cloud` implementation will be migrated here in a
separate change.
