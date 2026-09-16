# The container image: an ECR repository, and the build that fills it.
#
# This follows the same division of labour as the EC2 route in app.tf — you
# produce the artifact, Terraform ships it — except that the build is invoked
# here rather than typed by hand, because an image has to exist before the
# pods referencing it will start. build_image = false turns that off; app_image
# skips the repository altogether.

locals {
  # The repository root, two directories up from infra/terraform.
  app_root = abspath("${path.module}/../..")

  # Everything the image is built from. webapp/dist is in the list because it is
  # embedded into the binary, so a rebuilt React app is a new server image; the
  # React *sources* are not, since they reach the image only through dist.
  # tests/ is deliberately absent: it is not compiled into the image, so a
  # change there should not churn the tag and roll the pods.
  image_source_files = sort(tolist(setunion(
    fileset(local.app_root, "*.go"),
    fileset(local.app_root, "go.mod"),
    fileset(local.app_root, "go.sum"),
    fileset(local.app_root, "Dockerfile"),
    fileset(local.app_root, "cmd/**"),
    fileset(local.app_root, "internal/**"),
    fileset(local.app_root, "migrate/**"),
    fileset(local.app_root, "templates/**"),
    fileset(local.app_root, "webapp/dist/**"),
  )))

  # A tag derived from the sources, so that changing the server changes the
  # image reference in the Deployment, which is what actually triggers a rollout.
  # A fixed tag like "latest" would leave Kubernetes seeing no change at all.
  image_source_hash = substr(sha1(join("", [
    for f in local.image_source_files : filesha1("${local.app_root}/${f}")
  ])), 0, 12)

  image_tag = var.app_image_tag != "" ? var.app_image_tag : local.image_source_hash

  ecr_repository_url = var.deploy_eks ? aws_ecr_repository.app[0].repository_url : ""

  # What the pods actually run.
  app_image = var.app_image != "" ? var.app_image : "${local.ecr_repository_url}:${local.image_tag}"

  # Nodes are x86_64 on t3/t2 and arm64 on t4g. The image has to match, and a
  # laptop building it is often neither.
  image_platform = startswith(var.eks_node_instance_type, "t4g.") || startswith(var.eks_node_instance_type, "m6g.") ? "linux/arm64" : "linux/amd64"
}

resource "aws_ecr_repository" "app" {
  count = var.deploy_eks && var.app_image == "" ? 1 : 0

  name                 = var.name
  image_tag_mutability = "MUTABLE"
  # So that `terraform destroy` does not stop on "repository contains images".
  force_delete = true

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Untagged layers left behind by repeated builds cost storage and nothing else.
resource "aws_ecr_lifecycle_policy" "app" {
  count      = var.deploy_eks && var.app_image == "" ? 1 : 0
  repository = aws_ecr_repository.app[0].name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after a day"
      selection = {
        tagStatus   = "untagged"
        countType   = "sinceImagePushed"
        countUnit   = "days"
        countNumber = 1
      }
      action = { type = "expire" }
    }]
  })
}

# docker build and docker push, driven by build-and-push.sh so that the same
# thing can be run by hand when something goes wrong. triggers_replace means it
# re-runs when the sources change and does nothing when they have not.
resource "terraform_data" "image" {
  count = var.deploy_eks && var.app_image == "" && var.build_image ? 1 : 0

  triggers_replace = {
    repository = aws_ecr_repository.app[0].repository_url
    tag        = local.image_tag
    platform   = local.image_platform
  }

  provisioner "local-exec" {
    command     = "${path.module}/build-and-push.sh"
    working_dir = path.module

    environment = {
      ECR_REPOSITORY = aws_ecr_repository.app[0].repository_url
      IMAGE_TAG      = local.image_tag
      PLATFORM       = local.image_platform
      BUILD_CONTEXT  = local.app_root
      AWS_REGION     = var.region
      AWS_PROFILE    = var.profile
    }
  }
}
