# The EKS cluster, its node group, and the OIDC provider that lets pods hold
# IAM roles.
#
# A warning this file cannot repeat often enough: none of this is free tier.
# The control plane is billed at roughly $0.10 an hour whether or not anything
# is running on it — about $73 a month — and the ALB in alb_controller.tf adds
# another $16 or so. The node group itself is ordinary EC2. The database next
# door stays free; this does not. `terraform destroy` is the off switch.
#
# Everything here is created only when deploy_eks is true.

locals {
  eks_count        = var.deploy_eks ? 1 : 0
  cluster_name     = "${var.name}-eks"
  eks_api_cidrs    = length(var.eks_public_access_cidrs) > 0 ? var.eks_public_access_cidrs : local.allowed_cidrs
  eks_cluster_tags = { Name = local.cluster_name }

  # The default VPC has a subnet in every availability zone, and EKS will not
  # put a control plane in every availability zone — see eks_excluded_azs. The
  # unusable ones are dropped here rather than at the resource, so that the
  # subnet tags below land only on subnets the cluster actually uses.
  eks_subnet_ids = [
    for subnet in data.aws_subnet.eks :
    subnet.id if !contains(var.eks_excluded_azs, subnet.availability_zone)
  ]

  eks_subnet_azs = sort([
    for subnet in data.aws_subnet.eks :
    subnet.availability_zone if !contains(var.eks_excluded_azs, subnet.availability_zone)
  ])
}

# The default VPC's subnets, one per availability zone. default-for-az filters
# out Local Zone and Wavelength subnets, which EKS will not place nodes in and
# which would otherwise fail the "two availability zones" requirement in a
# confusing way.
data "aws_subnets" "eks" {
  count = local.eks_count

  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

# aws_subnets returns ids and nothing else, and the filtering above needs each
# subnet's availability zone, so each one is read individually.
data "aws_subnet" "eks" {
  for_each = var.deploy_eks ? toset(data.aws_subnets.eks[0].ids) : toset([])
  id       = each.value
}

# The load balancer controller finds subnets to put an ALB in by tag, and the
# default VPC's subnets come with no such tags. These two are what make
# auto-discovery work; they are removed again on destroy.
resource "aws_ec2_tag" "eks_subnet_elb_role" {
  for_each = toset(local.eks_subnet_ids)

  resource_id = each.value
  key         = "kubernetes.io/role/elb"
  value       = "1"
}

resource "aws_ec2_tag" "eks_subnet_cluster" {
  for_each = toset(local.eks_subnet_ids)

  resource_id = each.value
  key         = "kubernetes.io/cluster/${local.cluster_name}"
  value       = "shared"
}

# ---------------------------------------------------------------------------
# IAM: one role for the control plane, one for the nodes
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "assume_eks" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_cluster" {
  count              = local.eks_count
  name               = "${var.name}-eks-cluster"
  assume_role_policy = data.aws_iam_policy_document.assume_eks.json
}

resource "aws_iam_role_policy_attachment" "eks_cluster" {
  count      = local.eks_count
  role       = aws_iam_role.eks_cluster[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_iam_role" "eks_node" {
  count              = local.eks_count
  name               = "${var.name}-eks-node"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
}

resource "aws_iam_role_policy_attachment" "eks_node" {
  for_each = var.deploy_eks ? toset([
    "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
    # Nodes pull the server's image from the ECR repository in ecr.tf.
    "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
    # Session Manager, for the same reason the EC2 route has it: a look around
    # without an SSH key or an open port 22.
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ]) : toset([])

  role       = aws_iam_role.eks_node[0].name
  policy_arn = each.value
}

# ---------------------------------------------------------------------------
# The cluster
# ---------------------------------------------------------------------------

resource "aws_eks_cluster" "this" {
  count = local.eks_count

  name     = local.cluster_name
  role_arn = aws_iam_role.eks_cluster[0].arn
  # Empty means "whatever EKS defaults to", which avoids pinning a version that
  # has since gone out of support.
  version = var.eks_version != "" ? var.eks_version : null

  vpc_config {
    subnet_ids = local.eks_subnet_ids
    # Public, because kubectl and the Kubernetes provider run from a laptop
    # outside the VPC — but only from eks_api_cidrs, which defaults to this
    # machine, in the same spirit as the database's security group.
    endpoint_public_access  = true
    endpoint_private_access = true
    public_access_cidrs     = local.eks_api_cidrs
  }

  access_config {
    # The API is the modern way to say who is a cluster administrator; the
    # bootstrap flag grants it to whoever runs this apply, which is what lets
    # the Kubernetes provider create anything at all.
    authentication_mode                         = "API_AND_CONFIG_MAP"
    bootstrap_cluster_creator_admin_permissions = true
  }

  tags = local.eks_cluster_tags

  lifecycle {
    # EKS needs two availability zones. Saying so here turns an over-eager
    # eks_excluded_azs into a plan-time message rather than a CreateCluster
    # failure twenty minutes into an apply.
    precondition {
      condition     = length(local.eks_subnet_ids) >= 2
      error_message = "EKS needs subnets in at least two availability zones; after removing ${join(", ", var.eks_excluded_azs)} only ${length(local.eks_subnet_ids)} remain. Check eks_excluded_azs."
    }
  }

  # Deleting the role before the cluster leaves the cluster undeletable, so the
  # attachment has to outlive it.
  depends_on = [aws_iam_role_policy_attachment.eks_cluster]
}

# ---------------------------------------------------------------------------
# IRSA: the trust relationship that lets a service account assume a role
# ---------------------------------------------------------------------------

data "tls_certificate" "eks_oidc" {
  count = local.eks_count
  url   = aws_eks_cluster.this[0].identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "eks" {
  count = local.eks_count

  url             = aws_eks_cluster.this[0].identity[0].oidc[0].issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.eks_oidc[0].certificates[0].sha1_fingerprint]
}

# ---------------------------------------------------------------------------
# Nodes
# ---------------------------------------------------------------------------

resource "aws_eks_node_group" "this" {
  count = local.eks_count

  cluster_name    = aws_eks_cluster.this[0].name
  node_group_name = "${var.name}-nodes"
  node_role_arn   = aws_iam_role.eks_node[0].arn
  subnet_ids      = local.eks_subnet_ids

  instance_types = [var.eks_node_instance_type]
  capacity_type  = "ON_DEMAND"
  disk_size      = 20

  scaling_config {
    desired_size = var.eks_node_desired_count
    min_size     = var.eks_node_min_count
    max_size     = var.eks_node_max_count
  }

  update_config {
    max_unavailable = 1
  }

  tags = {
    Name = "${var.name}-node"
  }

  lifecycle {
    # The node count is the autoscaler's business once the group exists, not
    # something to argue with on every apply.
    ignore_changes = [scaling_config[0].desired_size]
  }

  depends_on = [aws_iam_role_policy_attachment.eks_node]
}

# ---------------------------------------------------------------------------
# Add-ons
# ---------------------------------------------------------------------------
#
# vpc-cni gives pods VPC addresses, which is what lets the ALB send traffic
# straight to a pod rather than through a node port. CoreDNS has to wait for
# nodes: it is an ordinary deployment and has nowhere to run before then.

resource "aws_eks_addon" "vpc_cni" {
  count                       = local.eks_count
  cluster_name                = aws_eks_cluster.this[0].name
  addon_name                  = "vpc-cni"
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"
}

resource "aws_eks_addon" "kube_proxy" {
  count                       = local.eks_count
  cluster_name                = aws_eks_cluster.this[0].name
  addon_name                  = "kube-proxy"
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"
}

resource "aws_eks_addon" "coredns" {
  count                       = local.eks_count
  cluster_name                = aws_eks_cluster.this[0].name
  addon_name                  = "coredns"
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"

  depends_on = [aws_eks_node_group.this]
}
