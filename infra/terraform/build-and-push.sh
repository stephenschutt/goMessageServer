#!/usr/bin/env bash
# Build the server image and push it to ECR.
#
# Terraform runs this from ecr.tf during apply, passing everything through the
# environment. It is a script rather than an inline command so that it can be
# run by hand when an apply fails here, which is where most first-time trouble
# with this configuration shows up:
#
#   ECR_REPOSITORY=<account>.dkr.ecr.us-east-1.amazonaws.com/gomessageserver \
#   IMAGE_TAG=dev PLATFORM=linux/amd64 BUILD_CONTEXT=../.. AWS_REGION=us-east-1 \
#   ./build-and-push.sh
set -euo pipefail

: "${ECR_REPOSITORY:?ECR_REPOSITORY is required}"
: "${IMAGE_TAG:?IMAGE_TAG is required}"
: "${AWS_REGION:?AWS_REGION is required}"
PLATFORM="${PLATFORM:-linux/amd64}"
BUILD_CONTEXT="${BUILD_CONTEXT:-../..}"

# An empty AWS_PROFILE is how Terraform spells "use the default credential
# chain", but to the aws CLI it means "a profile named empty string".
if [ -z "${AWS_PROFILE:-}" ]; then
  unset AWS_PROFILE
fi

registry="${ECR_REPOSITORY%%/*}"

# Both of these fail further down in ways that name the symptom rather than the
# cause: a broken aws CLI surfaces as "password is empty" from docker login, and
# a stopped Docker daemon as a connection refused halfway through a build.
if ! aws --version >/dev/null 2>&1; then
  echo "error: the aws CLI on PATH does not run." >&2
  echo "       $(command -v aws || echo 'aws: not found')" >&2
  echo "       On Apple Silicon this is usually an x86_64 build: brew install awscli" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "error: cannot reach the Docker daemon. Start Docker Desktop and retry." >&2
  exit 1
fi

echo "==> Signing in to ${registry}"
if ! aws ecr get-login-password --region "$AWS_REGION" |
  docker login --username AWS --password-stdin "$registry"; then
  echo "error: could not sign in to ${registry}." >&2
  echo "       Check that your AWS credentials are valid: aws sts get-caller-identity" >&2
  exit 1
fi

echo "==> Building ${ECR_REPOSITORY}:${IMAGE_TAG} for ${PLATFORM}"
# buildx when it is available: it is what cross-compiles an amd64 image on an
# Apple Silicon laptop and pushes in the same step. Plain build otherwise.
if docker buildx version >/dev/null 2>&1; then
  docker buildx build \
    --platform "$PLATFORM" \
    --tag "${ECR_REPOSITORY}:${IMAGE_TAG}" \
    --push \
    "$BUILD_CONTEXT"
else
  docker build \
    --platform "$PLATFORM" \
    --tag "${ECR_REPOSITORY}:${IMAGE_TAG}" \
    "$BUILD_CONTEXT"
  docker push "${ECR_REPOSITORY}:${IMAGE_TAG}"
fi

echo "==> Pushed ${ECR_REPOSITORY}:${IMAGE_TAG}"
