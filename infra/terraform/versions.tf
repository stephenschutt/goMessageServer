# Provider and version pins.
#
# Terraform 1.9 is the floor because the variable validation in variables.tf
# refers to other variables, which older versions reject.

terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
    # The three below are only used when deploy_eks is on: tls to read the
    # cluster's OIDC thumbprint, and kubernetes and helm to put workloads on it.
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
    helm = {
      source = "hashicorp/helm"
      # Held at 2.x deliberately: 3.0 replaced the `set { }` blocks used in
      # alb_controller.tf with a different attribute shape.
      version = "~> 2.17"
    }
  }
}
