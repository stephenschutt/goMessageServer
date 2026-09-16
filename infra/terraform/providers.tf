# The AWS account this points at is whichever one the CLI is already signed in
# to: set `profile` (or AWS_PROFILE) to choose between several. The account id
# is an output, so a plan can be checked against the console before it is
# applied.

provider "aws" {
  region = var.region
  # An empty string means "use the default credential chain" — environment
  # variables, SSO, or the default profile — rather than a profile named "".
  profile = var.profile != "" ? var.profile : null

  default_tags {
    tags = merge({
      Project   = var.name
      ManagedBy = "terraform"
    }, var.tags)
  }
}

data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

# ---------------------------------------------------------------------------
# Kubernetes and Helm
# ---------------------------------------------------------------------------
#
# Both talk to the cluster created in eks.tf. try() covers the deploy_eks =
# false case: with no cluster in state these expressions have nothing to read,
# and a provider with no resources to manage is never configured, so an empty
# host is never used.
#
# Authentication is an exec plugin rather than a token from
# aws_eks_cluster_auth, because a token read at plan time can expire part-way
# through a long apply. It does mean the aws CLI has to be on PATH.

locals {
  eks_exec_args = concat(
    ["eks", "get-token", "--cluster-name", try(aws_eks_cluster.this[0].name, ""), "--region", var.region],
    var.profile != "" ? ["--profile", var.profile] : [],
  )
}

provider "kubernetes" {
  host                   = try(aws_eks_cluster.this[0].endpoint, "")
  cluster_ca_certificate = try(base64decode(aws_eks_cluster.this[0].certificate_authority[0].data), "")

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = local.eks_exec_args
  }
}

provider "helm" {
  kubernetes {
    host                   = try(aws_eks_cluster.this[0].endpoint, "")
    cluster_ca_certificate = try(base64decode(aws_eks_cluster.this[0].certificate_authority[0].data), "")

    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = local.eks_exec_args
    }
  }
}
