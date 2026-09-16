# The AWS Load Balancer Controller: the piece that turns the Ingress in
# kubernetes.tf into an actual Application Load Balancer.
#
# Without it, the only load balancer Kubernetes can ask AWS for is the NLB
# behind a Service of type LoadBalancer. REQ-008 asks for an ALB, and an ALB on
# EKS means this controller watching Ingress objects and calling the ELBv2 API
# itself — which is why it needs an IAM role of its own.

locals {
  alb_service_account = "aws-load-balancer-controller"
}

# IRSA: the controller's service account, and only that one, may assume this
# role. The audience and subject conditions are what stop any other pod in the
# cluster from doing the same.
data "aws_iam_policy_document" "alb_controller_assume" {
  count = local.eks_count

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.eks[0].arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = ["system:serviceaccount:kube-system:${local.alb_service_account}"]
    }
  }
}

locals {
  # The issuer without its scheme, which is the spelling the condition keys use.
  oidc_host = var.deploy_eks ? replace(aws_eks_cluster.this[0].identity[0].oidc[0].issuer, "https://", "") : ""
}

resource "aws_iam_role" "alb_controller" {
  count              = local.eks_count
  name               = "${var.name}-alb-controller"
  assume_role_policy = data.aws_iam_policy_document.alb_controller_assume[0].json
}

# Kept in policies/alb-controller.json rather than fetched from GitHub at plan
# time: an apply should not depend on a URL, and a permissions document is
# exactly the kind of thing that ought to be reviewable in a diff.
resource "aws_iam_policy" "alb_controller" {
  count       = local.eks_count
  name        = "${var.name}-alb-controller"
  description = "AWS Load Balancer Controller for ${local.cluster_name}"
  policy      = file("${path.module}/policies/alb-controller.json")
}

resource "aws_iam_role_policy_attachment" "alb_controller" {
  count      = local.eks_count
  role       = aws_iam_role.alb_controller[0].name
  policy_arn = aws_iam_policy.alb_controller[0].arn
}

# Created here rather than by the chart so that it carries the role annotation
# from the moment it exists.
resource "kubernetes_service_account_v1" "alb_controller" {
  count = local.eks_count

  metadata {
    name      = local.alb_service_account
    namespace = "kube-system"

    labels = {
      "app.kubernetes.io/name"       = local.alb_service_account
      "app.kubernetes.io/component"  = "controller"
      "app.kubernetes.io/managed-by" = "terraform"
    }

    annotations = {
      "eks.amazonaws.com/role-arn" = aws_iam_role.alb_controller[0].arn
    }
  }

  depends_on = [aws_eks_node_group.this]
}

resource "helm_release" "alb_controller" {
  count = local.eks_count

  name       = local.alb_service_account
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  namespace  = "kube-system"
  version    = var.alb_controller_chart_version != "" ? var.alb_controller_chart_version : null

  # The controller's webhooks have to be answering before an Ingress will be
  # admitted, and the Ingress below is created in the same apply.
  wait = true

  set {
    name  = "clusterName"
    value = aws_eks_cluster.this[0].name
  }

  set {
    name  = "serviceAccount.create"
    value = "false"
  }

  set {
    name  = "serviceAccount.name"
    value = local.alb_service_account
  }

  # Given explicitly rather than discovered from instance metadata, which is one
  # fewer thing to go wrong on a cluster this small.
  set {
    name  = "region"
    value = var.region
  }

  set {
    name  = "vpcId"
    value = data.aws_vpc.default.id
  }

  # Two replicas would not both fit while the node group is at its minimum.
  set {
    name  = "replicaCount"
    value = "1"
  }

  depends_on = [
    kubernetes_service_account_v1.alb_controller,
    aws_iam_role_policy_attachment.alb_controller,
    aws_eks_addon.coredns,
  ]
}
