#!/usr/bin/env bash
# Existing infrastructure only. Works with GitHub OIDC or AWS_PROFILE=unifinsg.
set -euo pipefail
cd "$(dirname "$0")/.."
for variable in EXPECTED_ACCOUNT_ID AWS_REGION ECR_REPOSITORY ECS_CLUSTER ECS_SERVICE FRONTEND_BUCKET CLOUDFRONT_DISTRIBUTION_ID PUBLIC_URL RELEASE_ID; do
  if [[ -z "${!variable:-}" ]]; then echo "Missing $variable" >&2; exit 1; fi
done
[[ "$RELEASE_ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$ ]] || { echo 'Invalid release ID'; exit 1; }
[[ "$PUBLIC_URL" == https://* ]] || { echo 'PUBLIC_URL must use HTTPS'; exit 1; }
export AWS_PAGER=''
account=$(aws sts get-caller-identity --query Account --output text)
[[ "$account" == "$EXPECTED_ACCOUNT_ID" ]] || { echo 'Wrong AWS account'; exit 1; }
mkdir -p deploy/output
registry="$account.dkr.ecr.$AWS_REGION.amazonaws.com"
image="$registry/$ECR_REPOSITORY:$RELEASE_ID"

# Validate all local artifacts before changing the running service.
(cd frontend && npm ci && VITE_API_BASE_URL=/api VITE_AUTH_MODE=cognito VITE_USE_MOCK=false npm run build)
tar -czf deploy/output/frontend.tar.gz -C frontend/dist .
docker build --platform linux/amd64 -t "$image" backend
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$registry"
docker push "$image"
digest=$(aws ecr describe-images --repository-name "$ECR_REPOSITORY" --image-ids "imageTag=$RELEASE_ID" --query 'imageDetails[0].imageDigest' --output text)
image="$registry/$ECR_REPOSITORY@$digest"
previous=$(aws ecs describe-services --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE" --query 'services[0].taskDefinition' --output text)
[[ "$previous" == arn:aws:ecs:* ]] || { echo 'Existing ECS service is required'; exit 1; }
printf '%s\n' "$previous" > deploy/output/previous-task-definition.txt
aws ecs describe-task-definition --task-definition "$previous" --query taskDefinition > deploy/output/task-definition.json
jq --arg image "$image" '
  del(.taskDefinitionArn,.revision,.status,.requiresAttributes,.compatibilities,.registeredAt,.registeredBy,.deregisteredAt)
  | if ([.containerDefinitions[] | select(.name == "backend")] | length) != 1 then error("Exactly one backend container is required") else . end
  | (.containerDefinitions[] | select(.name == "backend") | .image) = $image
' deploy/output/task-definition.json > deploy/output/next-task-definition.json
next=$(aws ecs register-task-definition --cli-input-json file://deploy/output/next-task-definition.json --query 'taskDefinition.taskDefinitionArn' --output text)
printf '%s\n' "$next" > deploy/output/new-task-definition.txt
aws ecs update-service --cluster "$ECS_CLUSTER" --service "$ECS_SERVICE" --task-definition "$next" --deployment-configuration 'deploymentCircuitBreaker={enable=true,rollback=true},minimumHealthyPercent=100,maximumPercent=200' > /dev/null
aws ecs wait services-stable --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE"
actual=$(aws ecs describe-services --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE" --query 'services[0].taskDefinition' --output text)
[[ "$actual" == "$next" ]] || { echo 'Deployment rolled back; frontend was not published'; exit 1; }
curl --fail --silent --show-error --retry 5 --retry-all-errors --retry-delay 5 "$PUBLIC_URL/api/readyz"

# Keep old hashed assets for cached pages and rollback. Publish index last.
aws s3 sync frontend/dist/ "s3://$FRONTEND_BUCKET/" --exclude index.html --exclude 'assets/*' --cache-control 'no-cache'
aws s3 sync frontend/dist/assets/ "s3://$FRONTEND_BUCKET/assets/" --cache-control 'public,max-age=31536000,immutable'
aws s3 cp frontend/dist/index.html "s3://$FRONTEND_BUCKET/index.html" --cache-control 'no-cache' --content-type 'text/html'
invalidation=$(aws cloudfront create-invalidation --distribution-id "$CLOUDFRONT_DISTRIBUTION_ID" --paths '/*' --query Invalidation.Id --output text)
aws cloudfront wait invalidation-completed --distribution-id "$CLOUDFRONT_DISTRIBUTION_ID" --id "$invalidation"
curl --fail --silent --show-error --output /dev/null "$PUBLIC_URL/"
echo 'Deployment and readiness checks passed.'
