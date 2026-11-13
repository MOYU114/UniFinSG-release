#!/usr/bin/env python3
"""Finish the managed prod stack without exposing secrets in shell arguments.

AWS_BIN=/path/to/aws python deploy/first-deploy.py sync-secrets
AWS_BIN=/path/to/aws python deploy/first-deploy.py init-db --image REGISTRY/IMAGE@sha256:DIGEST
AWS_BIN=/path/to/aws python deploy/first-deploy.py service-template --image REGISTRY/IMAGE@sha256:DIGEST
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
AWS = os.getenv("AWS_BIN") or shutil.which("aws")
PROFILE = os.getenv("AWS_PROFILE", "unifinsg")
REGION = "ap-southeast-1"


def aws(*args, payload=None):
    if not AWS:
        raise RuntimeError("Set AWS_BIN or put AWS CLI v2 in PATH")
    cmd = [AWS, *args, "--profile", PROFILE, "--region", REGION, "--no-cli-pager", "--output", "json"]
    if payload is not None:
        # macOS AWS CLI can consume stdin before loading /dev/stdin. Use a
        # private, automatically deleted request file; never put secrets in argv.
        with tempfile.NamedTemporaryFile(mode="w", prefix="unifinsg-aws-", suffix=".json") as request:
            json.dump(payload, request)
            request.flush()
            result = subprocess.run(cmd + ["--cli-input-json", "file://" + request.name], capture_output=True, text=True)
    else:
        result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode:
        # Client errors can echo request data; do not print payloads or stderr.
        raise RuntimeError("AWS operation failed: " + " ".join(args[:2]) + "; inspect the service status without exposing secrets")
    return json.loads(result.stdout) if result.stdout.strip() else {}


def outputs():
    identity = aws("sts", "get-caller-identity")
    if identity["Account"] != "815194656290":
        raise RuntimeError("Wrong AWS account")
    stack = aws("cloudformation", "describe-stacks", "--stack-name", "unifinsg-prod-core")["Stacks"][0]
    if stack["StackStatus"] not in ("CREATE_COMPLETE", "UPDATE_COMPLETE"):
        raise RuntimeError("Core stack is not ready: " + stack["StackStatus"])
    return {item["OutputKey"]: item["OutputValue"] for item in stack["Outputs"]}


def network(o):
    # Public egress avoids a NAT gateway; there is no public app ingress.
    return {"awsvpcConfiguration": {"subnets": [o["PublicSubnetA"], o["PublicSubnetB"]], "securityGroups": [o["AppSecurityGroup"]], "assignPublicIp": "ENABLED"}}


def log_config(o, prefix):
    return {"logDriver": "awslogs", "options": {"awslogs-group": o["Logs"], "awslogs-region": REGION, "awslogs-stream-prefix": prefix}}


def secrets(o, names):
    return [{"name": name, "valueFrom": o[key + "Arn"] + suffix} for name, key, suffix in names]


def task_base(o):
    return {"networkMode": "awsvpc", "requiresCompatibilities": ["FARGATE"], "cpu": "256", "memory": "512", "executionRoleArn": o["ExecutionRoleArn"], "taskRoleArn": o["TaskRoleArn"], "runtimePlatform": {"cpuArchitecture": "X86_64", "operatingSystemFamily": "LINUX"}}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("stage", choices=["sync-secrets", "configure-model", "model-check", "init-db", "service-template"])
    parser.add_argument("--image")
    args = parser.parse_args()
    o = outputs()
    if args.stage == "sync-secrets":
        client = aws("cognito-idp", "describe-user-pool-client", "--user-pool-id", o["UserPoolId"], "--client-id", o["UserPoolClientId"])["UserPoolClient"]
        aws("secretsmanager", "put-secret-value", payload={"SecretId": o["CognitoSecretArn"], "SecretString": client["ClientSecret"]})
        print("Cognito secret stored; value not displayed")
        return
    if args.stage == "configure-model":
        values = {}
        for line in (ROOT / ".env").read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                name, value = line.split("=", 1)
                values[name.strip()] = value.strip().strip("\"'")
        key = values.get("LLM_API_KEY") or values.get("GOOGLE_API_KEY")
        if not key:
            raise RuntimeError("No model API key in the ignored .env file")
        config = {"kind": "openai_compatible", "base_url": "https://generativelanguage.googleapis.com/v1beta/openai/", "model": "gemini-3-flash-preview", "api_key": key}
        aws("secretsmanager", "put-secret-value", payload={"SecretId": o["PlatformModelArn"], "SecretString": json.dumps(config)})
        print("Gemini platform configuration stored; key not displayed")
        return
    if not args.image or not args.image.startswith("815194656290.dkr.ecr.ap-southeast-1.amazonaws.com/unifinsg-prod-backend@sha256:"):
        raise RuntimeError("An immutable image digest from the project ECR repository is required")
    if args.stage in ("init-db", "model-check"):
        task = task_base(o)
        container = {
            "name": "database-init", "image": args.image, "essential": True,
            "command": ["./cloud-init"], "readonlyRootFilesystem": True,
            "environment": [{"name": "MYSQL_HOST", "value": o["DatabaseHost"]}],
            "secrets": secrets(o, [("MYSQL_ADMIN_PASSWORD", "AdminPassword", ":password::"), ("MYSQL_UNICHATGO_PASSWD", "DatabasePassword", ":password::")]),
            "logConfiguration": log_config(o, "init"),
        }
        if args.stage == "model-check":
            container.update(name="model-check", command=["/bin/sh", "-c", "./verify-llm.test -test.run TestGeminiLive -test.v -test.timeout 3m && ./verify-agent.test -test.run TestGeminiReceiptLive -test.v -test.timeout 3m"], environment=[], secrets=secrets(o, [("GOOGLE_API_KEY", "PlatformModel", ":api_key::")]), logConfiguration=log_config(o, "model-check"))
        task.update(family="unifinsg-prod-" + args.stage, containerDefinitions=[container])
        definition = aws("ecs", "register-task-definition", payload=task)["taskDefinition"]["taskDefinitionArn"]
        result = aws("ecs", "run-task", payload={"cluster": o["Cluster"], "taskDefinition": definition, "launchType": "FARGATE", "platformVersion": "LATEST", "count": 1, "networkConfiguration": network(o), "tags": [{"key": "project", "value": "unifinsg"}]})
        if result.get("failures") or not result.get("tasks"):
            raise RuntimeError("One-off initialization/verification task could not start")
        print(json.dumps({"task": result["tasks"][0]["taskArn"], "cluster": o["Cluster"]}))
        return

    values = {
        "GIN_MODE": "release", "AUTH_MODE": "cognito", "UNICHATGO_DB": "mysql", "SERVER_ADDRESS": ":8090",
        "MYSQL_HOST": o["DatabaseHost"], "MYSQL_PORT": "3306", "MYSQL_USER": "unichatgo", "MYSQL_DATABASE": "unichatgo",
        "MYSQL_PARAMS": "charset=utf8mb4&parseTime=true&loc=UTC&tls=true",
        "REDIS_HOST": o["RedisHost"], "REDIS_PORT": "6379", "REDIS_DB": "0", "REDIS_TLS": "true",
        "COGNITO_REGION": REGION, "COGNITO_USER_POOL_ID": o["UserPoolId"], "COGNITO_CLIENT_ID": o["UserPoolClientId"],
        "COGNITO_DOMAIN": o["CognitoDomain"], "COGNITO_REDIRECT_URI": o["PublicUrl"] + "/api/auth/callback", "FRONTEND_URL": o["PublicUrl"],
        "TRUSTED_PROXY_CIDRS": "10.42.0.0/16", "LLM_ALLOW_INSECURE_BYOK": "false",
        "ASSISTANT_RPM": "5", "CREDITS_DAILY_GRANT": "100", "AGENT_MAX_STEPS": "6", "AGENT_DRAFT_TTL_MINUTES": "30",
        "STORAGE_DRIVER": "s3", "S3_BUCKET": o["UploadBucket"], "S3_REGION": REGION, "S3_PREFIX": "uploads/", "UPLOAD_TTL_HOURS": "24",
    }
    task = task_base(o)
    task.update(family="unifinsg-prod-backend", containerDefinitions=[{
        "name": "backend", "image": args.image, "essential": True, "stopTimeout": 120,
        "portMappings": [{"containerPort": 8090, "protocol": "tcp"}],
        "environment": [{"name": name, "value": value} for name, value in values.items()],
        "secrets": secrets(o, [("MYSQL_UNICHATGO_PASSWD", "DatabasePassword", ":password::"), ("REDIS_PASSWD", "RedisPassword", ""), ("UNICHATGO_APIKEY_KEY", "ApplicationKey", ""), ("ADMIN_TOKEN", "AdminToken", ""), ("COGNITO_CLIENT_SECRET", "CognitoSecret", ""), ("LLM_KIND", "PlatformModel", ":kind::"), ("LLM_BASE_URL", "PlatformModel", ":base_url::"), ("LLM_MODEL", "PlatformModel", ":model::"), ("LLM_API_KEY", "PlatformModel", ":api_key::")]),
        "logConfiguration": log_config(o, "ecs"),
    }])
    # Convert the ECS API keys to CloudFormation property names.
    def cfn(value):
        if isinstance(value, list):
            return [cfn(item) for item in value]
        if isinstance(value, dict):
            special = {"readonlyRootFilesystem": "ReadonlyRootFilesystem", "cpuArchitecture": "CpuArchitecture", "operatingSystemFamily": "OperatingSystemFamily"}
            return {special.get(k, k[:1].upper() + k[1:]): (v if k == "options" else cfn(v)) for k, v in value.items()}
        return value

    definition = cfn(task)
    template = {"AWSTemplateFormatVersion": "2010-09-09", "Description": "UniFinSG prod app service using real managed dependencies and BYOK", "Resources": {
        "Task": {"Type": "AWS::ECS::TaskDefinition", "Properties": definition},
        "Service": {"Type": "AWS::ECS::Service", "Properties": {
            "Cluster": o["Cluster"], "ServiceName": "unifinsg-prod-backend", "TaskDefinition": {"Ref": "Task"}, "DesiredCount": 2,
            "LaunchType": "FARGATE", "PlatformVersion": "LATEST", "HealthCheckGracePeriodSeconds": 120,
            "NetworkConfiguration": cfn(network(o)), "LoadBalancers": [{"ContainerName": "backend", "ContainerPort": 8090, "TargetGroupArn": o["TargetGroup"]}],
            "DeploymentConfiguration": {"MinimumHealthyPercent": 100, "MaximumPercent": 200, "DeploymentCircuitBreaker": {"Enable": True, "Rollback": True}},
            "EnableECSManagedTags": True, "PropagateTags": "TASK_DEFINITION", "Tags": [{"Key": "project", "Value": "unifinsg"}, {"Key": "environment", "Value": "prod"}],
        }},
    }, "Outputs": {"ServiceName": {"Value": "unifinsg-prod-backend"}, "PublicUrl": {"Value": o["PublicUrl"]}}}
    output = ROOT / "deploy" / "output" / "cloud-service.json"
    output.parent.mkdir(exist_ok=True)
    output.write_text(json.dumps(template, indent=2) + "\n")
    print("Service template rendered: " + str(output))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, KeyError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
