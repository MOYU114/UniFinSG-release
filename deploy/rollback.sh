#!/usr/bin/env bash
set -euo pipefail
: "${EXPECTED_ACCOUNT_ID:?Set the expected account}"
: "${ECS_CLUSTER:?Set ECS_CLUSTER}"
: "${ECS_SERVICE:?Set ECS_SERVICE}"
: "${ROLLBACK_TASK_DEFINITION:?Use the previous-task-definition.txt release artifact}"
export AWS_PAGER=''
[[ "$(aws sts get-caller-identity --query Account --output text)" == "$EXPECTED_ACCOUNT_ID" ]] || { echo 'Wrong AWS account'; exit 1; }
aws ecs update-service --cluster "$ECS_CLUSTER" --service "$ECS_SERVICE" --task-definition "$ROLLBACK_TASK_DEFINITION" > /dev/null
aws ecs wait services-stable --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE"
actual=$(aws ecs describe-services --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE" --query 'services[0].taskDefinition' --output text)
[[ "$actual" == "$ROLLBACK_TASK_DEFINITION" ]] || { echo 'Rollback did not reach the requested revision'; exit 1; }
echo 'Backend rollback completed. Restore the previous frontend artifact separately if required.'
