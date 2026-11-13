#!/usr/bin/env python3
"""Render the first-deploy infrastructure; no AWS calls and no secret values."""
import json
from pathlib import Path


def ref(name):
    return {"Ref": name}


def attr(name, field):
    return {"Fn::GetAtt": [name, field]}


def sub(value):
    return {"Fn::Sub": value}


TAGS = [{"Key": "project", "Value": "unifinsg"}, {"Key": "environment", "Value": "prod"}]
resources = {}


def add(name, kind, properties, **options):
    resources[name] = {"Type": "AWS::" + kind, "Properties": properties, **options}


def secret(name, username=None, length=32):
    generated = {"PasswordLength": length, "ExcludePunctuation": True}
    if username:
        generated.update(SecretStringTemplate=json.dumps({"username": username}), GenerateStringKey="password")
    add(name, "SecretsManager::Secret", {"GenerateSecretString": generated, "Tags": TAGS})


def secret_ref(name, key=None):
    return sub("{{resolve:secretsmanager:${" + name + "}:SecretString" + (":" + key if key else "") + "}}")


add("Vpc", "EC2::VPC", {"CidrBlock": "10.42.0.0/16", "EnableDnsHostnames": True, "EnableDnsSupport": True, "Tags": TAGS})
add("Gateway", "EC2::InternetGateway", {"Tags": TAGS})
add("GatewayAttachment", "EC2::VPCGatewayAttachment", {"VpcId": ref("Vpc"), "InternetGatewayId": ref("Gateway")})
add("PublicRoutes", "EC2::RouteTable", {"VpcId": ref("Vpc"), "Tags": TAGS})
add("InternetRoute", "EC2::Route", {"RouteTableId": ref("PublicRoutes"), "DestinationCidrBlock": "0.0.0.0/0", "GatewayId": ref("Gateway")}, DependsOn="GatewayAttachment")
for zone, index in [("A", 0), ("B", 1)]:
    for scope, offset in [("Public", 0), ("Private", 10)]:
        name = scope + zone
        add(name, "EC2::Subnet", {"VpcId": ref("Vpc"), "AvailabilityZone": {"Fn::Select": [index, {"Fn::GetAZs": ""}]}, "CidrBlock": f"10.42.{offset + index}.0/24", "MapPublicIpOnLaunch": False, "Tags": TAGS})
        if scope == "Public":
            add(name + "Routes", "EC2::SubnetRouteTableAssociation", {"SubnetId": ref(name), "RouteTableId": ref("PublicRoutes")})
for name, description in [("AlbSecurity", "Private CloudFront origin"), ("AppSecurity", "Backend tasks"), ("DatabaseSecurity", "Private MySQL"), ("RedisSecurity", "Private TLS Redis")]:
    add(name, "EC2::SecurityGroup", {"GroupDescription": description, "VpcId": ref("Vpc"), "Tags": TAGS})
add("CloudFrontIngress", "EC2::SecurityGroupIngress", {"GroupId": ref("AlbSecurity"), "IpProtocol": "tcp", "FromPort": 80, "ToPort": 80, "SourcePrefixListId": ref("CloudFrontPrefixList")})
for name, destination, source, port in [("AppIngress", "AppSecurity", "AlbSecurity", 8090), ("DatabaseIngress", "DatabaseSecurity", "AppSecurity", 3306), ("RedisIngress", "RedisSecurity", "AppSecurity", 6379)]:
    add(name, "EC2::SecurityGroupIngress", {"GroupId": ref(destination), "SourceSecurityGroupId": ref(source), "IpProtocol": "tcp", "FromPort": port, "ToPort": port})

secret("AdminPassword", "dbadmin")
secret("DatabasePassword", "unichatgo")
secret("RedisPassword")
secret("ApplicationKey")
secret("AdminToken")
add("DatabaseSubnets", "RDS::DBSubnetGroup", {"DBSubnetGroupDescription": "UniFinSG private database", "SubnetIds": [ref("PrivateA"), ref("PrivateB")], "Tags": TAGS})
add("Database", "RDS::DBInstance", {
    "DBInstanceIdentifier": "unifinsg-prod-mysql", "Engine": "mysql", "EngineVersion": "8.4.13",
    "DBInstanceClass": "db.t4g.micro", "AllocatedStorage": "20", "StorageType": "gp3",
    "StorageEncrypted": True, "DBName": "unichatgo", "MasterUsername": "dbadmin",
    "MasterUserPassword": secret_ref("AdminPassword", "password"), "DBSubnetGroupName": ref("DatabaseSubnets"),
    "VPCSecurityGroups": [ref("DatabaseSecurity")], "PubliclyAccessible": False,
    "MultiAZ": False, "BackupRetentionPeriod": 1, "DeletionProtection": True,
    "AutoMinorVersionUpgrade": True, "CopyTagsToSnapshot": True,
    "EngineLifecycleSupport": "open-source-rds-extended-support-disabled", "Tags": TAGS,
}, DeletionPolicy="Snapshot", UpdateReplacePolicy="Snapshot")
add("RedisSubnets", "ElastiCache::SubnetGroup", {"Description": "UniFinSG private cache", "SubnetIds": [ref("PrivateA"), ref("PrivateB")]})
add("Redis", "ElastiCache::ReplicationGroup", {
    "ReplicationGroupId": "unifinsg-prod-redis", "ReplicationGroupDescription": "UniFinSG TLS session and credits store",
    "Engine": "redis", "EngineVersion": "7.1", "CacheNodeType": "cache.t4g.micro",
    "NumCacheClusters": 1, "AutomaticFailoverEnabled": False, "CacheSubnetGroupName": ref("RedisSubnets"),
    "SecurityGroupIds": [ref("RedisSecurity")], "AtRestEncryptionEnabled": True,
    "TransitEncryptionEnabled": True, "AuthToken": secret_ref("RedisPassword"), "Tags": TAGS,
})
for name, purpose in [("FrontendBucket", "frontend"), ("UploadBucket", "uploads")]:
    props = {"BucketName": sub("unifinsg-prod-" + purpose + "-${AWS::AccountId}"),
             "PublicAccessBlockConfiguration": {key: True for key in ["BlockPublicAcls", "BlockPublicPolicy", "IgnorePublicAcls", "RestrictPublicBuckets"]},
             "BucketEncryption": {"ServerSideEncryptionConfiguration": [{"ServerSideEncryptionByDefault": {"SSEAlgorithm": "AES256"}}]},
             "OwnershipControls": {"Rules": [{"ObjectOwnership": "BucketOwnerEnforced"}]}, "Tags": TAGS}
    if purpose == "uploads":
        props["LifecycleConfiguration"] = {"Rules": [{"Id": "expire-private-images", "Status": "Enabled", "Prefix": "uploads/", "ExpirationInDays": 1, "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 1}}]}
    else:
        props["VersioningConfiguration"] = {"Status": "Enabled"}
        props["LifecycleConfiguration"] = {"Rules": [{"Id": "old-frontend-versions", "Status": "Enabled", "NoncurrentVersionExpiration": {"NoncurrentDays": 7}}]}
    add(name, "S3::Bucket", props, DeletionPolicy="Retain", UpdateReplacePolicy="Retain")
add("Cluster", "ECS::Cluster", {"ClusterName": "unifinsg-prod", "ClusterSettings": [{"Name": "containerInsights", "Value": "disabled"}], "Tags": TAGS})
add("Logs", "Logs::LogGroup", {"LogGroupName": "/unifinsg/prod/backend", "RetentionInDays": 7, "Tags": TAGS})
add("Alb", "ElasticLoadBalancingV2::LoadBalancer", {"Name": "unifinsg-prod", "Scheme": "internal", "Type": "application", "Subnets": [ref("PrivateA"), ref("PrivateB")], "SecurityGroups": [ref("AlbSecurity")], "LoadBalancerAttributes": [{"Key": "idle_timeout.timeout_seconds", "Value": "180"}], "Tags": TAGS})
add("TargetGroup", "ElasticLoadBalancingV2::TargetGroup", {"Name": "unifinsg-prod-backend", "VpcId": ref("Vpc"), "Protocol": "HTTP", "Port": 8090, "TargetType": "ip", "HealthCheckPath": "/readyz", "Matcher": {"HttpCode": "200"}, "TargetGroupAttributes": [{"Key": "deregistration_delay.timeout_seconds", "Value": "30"}], "Tags": TAGS})
add("Listener", "ElasticLoadBalancingV2::Listener", {"LoadBalancerArn": ref("Alb"), "Port": 80, "Protocol": "HTTP", "DefaultActions": [{"Type": "forward", "TargetGroupArn": ref("TargetGroup")}]})
add("VpcOrigin", "CloudFront::VpcOrigin", {"VpcOriginEndpointConfig": {"Name": "unifinsg-prod-api", "Arn": ref("Alb"), "HTTPPort": 80, "HTTPSPort": 443, "OriginProtocolPolicy": "http-only"}}, DependsOn=["Listener", "GatewayAttachment"])
add("OriginAccess", "CloudFront::OriginAccessControl", {"OriginAccessControlConfig": {"Name": "unifinsg-prod-frontend", "OriginAccessControlOriginType": "s3", "SigningBehavior": "always", "SigningProtocol": "sigv4"}})
add("SpaFunction", "CloudFront::Function", {"Name": "unifinsg-prod-spa", "AutoPublish": True, "FunctionConfig": {"Comment": "Only rewrite SPA routes on the S3 origin", "Runtime": "cloudfront-js-2.0"}, "FunctionCode": (Path(__file__).parent / "cloudfront-spa.js").read_text()})
add("ApiOriginPolicy", "CloudFront::OriginRequestPolicy", {"OriginRequestPolicyConfig": {"Name": "unifinsg-prod-api", "CookiesConfig": {"CookieBehavior": "all"}, "QueryStringsConfig": {"QueryStringBehavior": "all"}, "HeadersConfig": {"HeaderBehavior": "allExcept", "Headers": ["host"]}}})
add("ApiCachePolicy", "CloudFront::CachePolicy", {"CachePolicyConfig": {
    "Name": "unifinsg-prod-api-no-cache", "MinTTL": 0, "DefaultTTL": 0, "MaxTTL": 0,
    "ParametersInCacheKeyAndForwardedToOrigin": {"CookiesConfig": {"CookieBehavior": "none"}, "HeadersConfig": {"HeaderBehavior": "none"}, "QueryStringsConfig": {"QueryStringBehavior": "none"}, "EnableAcceptEncodingGzip": False, "EnableAcceptEncodingBrotli": False},
}})
add("FrontendCachePolicy", "CloudFront::CachePolicy", {"CachePolicyConfig": {
    "Name": "unifinsg-prod-static", "MinTTL": 0, "DefaultTTL": 3600, "MaxTTL": 31536000,
    "ParametersInCacheKeyAndForwardedToOrigin": {"CookiesConfig": {"CookieBehavior": "none"}, "HeadersConfig": {"HeaderBehavior": "none"}, "QueryStringsConfig": {"QueryStringBehavior": "none"}, "EnableAcceptEncodingGzip": True, "EnableAcceptEncodingBrotli": True},
}})
add("Distribution", "CloudFront::Distribution", {"DistributionConfig": {
    "Enabled": True, "Comment": "UniFinSG prod", "DefaultRootObject": "index.html", "HttpVersion": "http2and3", "IPV6Enabled": True,
    "PriceClass": "PriceClass_All", "ViewerCertificate": {"CloudFrontDefaultCertificate": True},
    "Origins": [{"Id": "frontend", "DomainName": attr("FrontendBucket", "RegionalDomainName"), "S3OriginConfig": {"OriginAccessIdentity": ""}, "OriginAccessControlId": ref("OriginAccess")},
                {"Id": "api", "DomainName": attr("Alb", "DNSName"), "VpcOriginConfig": {"VpcOriginId": attr("VpcOrigin", "Id"), "OriginReadTimeout": 60, "OriginKeepaliveTimeout": 60}}],
    "DefaultCacheBehavior": {"TargetOriginId": "frontend", "ViewerProtocolPolicy": "redirect-to-https", "Compress": True, "CachePolicyId": ref("FrontendCachePolicy"), "FunctionAssociations": [{"EventType": "viewer-request", "FunctionARN": attr("SpaFunction", "FunctionARN")}]},
    "CacheBehaviors": [{"PathPattern": "/api/*", "TargetOriginId": "api", "ViewerProtocolPolicy": "https-only", "AllowedMethods": ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"], "CachePolicyId": ref("ApiCachePolicy"), "OriginRequestPolicyId": ref("ApiOriginPolicy"), "Compress": False}],
}, "Tags": TAGS})
add("FrontendPolicy", "S3::BucketPolicy", {"Bucket": ref("FrontendBucket"), "PolicyDocument": {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "cloudfront.amazonaws.com"}, "Action": "s3:GetObject", "Resource": sub("${FrontendBucket.Arn}/*"), "Condition": {"StringEquals": {"AWS:SourceArn": sub("arn:${AWS::Partition}:cloudfront::${AWS::AccountId}:distribution/${Distribution}")}}}, {"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": [attr("FrontendBucket", "Arn"), sub("${FrontendBucket.Arn}/*")], "Condition": {"Bool": {"aws:SecureTransport": "false"}}}]}})
add("UploadPolicy", "S3::BucketPolicy", {"Bucket": ref("UploadBucket"), "PolicyDocument": {"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": [attr("UploadBucket", "Arn"), sub("${UploadBucket.Arn}/*")], "Condition": {"Bool": {"aws:SecureTransport": "false"}}}]}})
add("UserPool", "Cognito::UserPool", {"UserPoolName": "unifinsg-prod", "UserPoolTier": "LITE", "UsernameAttributes": ["email"], "AutoVerifiedAttributes": ["email"], "Policies": {"PasswordPolicy": {"MinimumLength": 10, "RequireLowercase": True, "RequireUppercase": True, "RequireNumbers": True, "RequireSymbols": False}}, "AccountRecoverySetting": {"RecoveryMechanisms": [{"Name": "verified_email", "Priority": 1}]}, "UserPoolTags": {"project": "unifinsg", "environment": "prod"}})
add("UserPoolDomain", "Cognito::UserPoolDomain", {"Domain": sub("unifinsg-prod-${AWS::AccountId}"), "UserPoolId": ref("UserPool")})
add("UserPoolClient", "Cognito::UserPoolClient", {"ClientName": "unifinsg-prod-web", "UserPoolId": ref("UserPool"), "GenerateSecret": True, "AllowedOAuthFlowsUserPoolClient": True, "AllowedOAuthFlows": ["code"], "AllowedOAuthScopes": ["openid", "email"], "SupportedIdentityProviders": ["COGNITO"], "CallbackURLs": [sub("https://${Distribution.DomainName}/api/auth/callback")], "LogoutURLs": [sub("https://${Distribution.DomainName}")], "ExplicitAuthFlows": ["ALLOW_REFRESH_TOKEN_AUTH"], "EnableTokenRevocation": True, "PreventUserExistenceErrors": "ENABLED", "ReadAttributes": ["email", "email_verified"], "WriteAttributes": ["email"]})
# CloudFormation does not expose the client secret as a GetAtt attribute.
# first-deploy.py copies it from DescribeUserPoolClient without printing it.
secret("CognitoSecret")
add("PlatformModel", "SecretsManager::Secret", {"SecretString": json.dumps({"kind": "openai_compatible", "base_url": "", "model": "", "api_key": ""}), "Tags": TAGS})

def role(name, statements):
    add(name, "IAM::Role", {"AssumeRolePolicyDocument": {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "ecs-tasks.amazonaws.com"}, "Action": "sts:AssumeRole"}]}, "Policies": [{"PolicyName": "project-resources", "PolicyDocument": {"Version": "2012-10-17", "Statement": statements}}], "Tags": TAGS})


role("ExecutionRole", [{"Effect": "Allow", "Action": ["ecr:GetAuthorizationToken"], "Resource": "*"}, {"Effect": "Allow", "Action": ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"], "Resource": sub("arn:${AWS::Partition}:ecr:${AWS::Region}:${AWS::AccountId}:repository/unifinsg-prod-backend")}, {"Effect": "Allow", "Action": ["logs:CreateLogStream", "logs:PutLogEvents"], "Resource": attr("Logs", "Arn")}, {"Effect": "Allow", "Action": "secretsmanager:GetSecretValue", "Resource": [ref(s) for s in ["AdminPassword", "DatabasePassword", "RedisPassword", "ApplicationKey", "AdminToken", "CognitoSecret", "PlatformModel"]]}])
role("TaskRole", [{"Effect": "Allow", "Action": ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"], "Resource": sub("${UploadBucket.Arn}/uploads/*")}, {"Effect": "Allow", "Action": "cognito-idp:AdminDeleteUser", "Resource": attr("UserPool", "Arn")}])

outputs = {name: {"Value": value} for name, value in {
    "VpcId": ref("Vpc"), "PublicSubnetA": ref("PublicA"), "PublicSubnetB": ref("PublicB"), "AppSecurityGroup": ref("AppSecurity"),
    "DatabaseHost": attr("Database", "Endpoint.Address"), "RedisHost": attr("Redis", "PrimaryEndPoint.Address"),
    "FrontendBucket": ref("FrontendBucket"), "UploadBucket": ref("UploadBucket"), "Cluster": ref("Cluster"), "TargetGroup": ref("TargetGroup"),
    "DistributionId": ref("Distribution"), "PublicUrl": sub("https://${Distribution.DomainName}"), "UserPoolId": ref("UserPool"), "UserPoolClientId": ref("UserPoolClient"),
    "CognitoDomain": sub("https://${UserPoolDomain}.auth.${AWS::Region}.amazoncognito.com"),
    "ExecutionRoleArn": attr("ExecutionRole", "Arn"), "TaskRoleArn": attr("TaskRole", "Arn"),
    "Logs": ref("Logs"), **{s + "Arn": ref(s) for s in ["AdminPassword", "DatabasePassword", "RedisPassword", "ApplicationKey", "AdminToken", "CognitoSecret", "PlatformModel"]},
}.items()}
template = {"AWSTemplateFormatVersion": "2010-09-09", "Description": "UniFinSG prod: real managed database/cache, private CloudFront origin and private uploads. No NAT gateway.", "Parameters": {"CloudFrontPrefixList": {"Type": "String", "Default": "pl-31a34658"}}, "Resources": resources, "Outputs": outputs}
destination = Path(__file__).parent / "cloud-core.json"
destination.write_text(json.dumps(template, indent=2) + "\n")
print(f"Rendered {len(resources)} resources: {destination}")
