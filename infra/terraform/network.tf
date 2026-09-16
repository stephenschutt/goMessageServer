# Everything sits in the account's default VPC. That is deliberate: a VPC of our
# own would want private subnets, and private subnets that can still reach the
# internet want a NAT gateway, which is the one thing here that would quietly
# cost about $32 a month. The default VPC's subnets are public and free.

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

# Looked up only when allowed_cidrs is empty, so the default is "just me" rather
# than "the whole internet". If this machine is behind a changing address, set
# allowed_cidrs explicitly instead.
data "http" "my_ip" {
  count = length(var.allowed_cidrs) == 0 ? 1 : 0
  url   = "https://checkip.amazonaws.com"
}

locals {
  allowed_cidrs = length(var.allowed_cidrs) > 0 ? var.allowed_cidrs : ["${chomp(data.http.my_ip[0].response_body)}/32"]
}

# ---------------------------------------------------------------------------
# Security groups
# ---------------------------------------------------------------------------

resource "aws_security_group" "database" {
  name        = "${var.name}-database"
  description = "Postgres access for ${var.name}"
  vpc_id      = data.aws_vpc.default.id
}

resource "aws_vpc_security_group_ingress_rule" "database_from_admin" {
  for_each = toset(local.allowed_cidrs)

  security_group_id = aws_security_group.database.id
  description       = "Postgres from ${each.value}"
  cidr_ipv4         = each.value
  from_port         = 5432
  to_port           = 5432
  ip_protocol       = "tcp"
}

# The server reaches the database by security group rather than by address, so
# replacing the instance does not mean editing a CIDR.
resource "aws_vpc_security_group_ingress_rule" "database_from_app" {
  count = var.deploy_app ? 1 : 0

  security_group_id            = aws_security_group.database.id
  description                  = "Postgres from the application instance"
  referenced_security_group_id = aws_security_group.app[0].id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
}

resource "aws_security_group" "app" {
  count = var.deploy_app ? 1 : 0

  name        = "${var.name}-app"
  description = "messageServer access for ${var.name}"
  vpc_id      = data.aws_vpc.default.id
}

resource "aws_vpc_security_group_ingress_rule" "app_from_admin" {
  for_each = var.deploy_app ? toset(local.allowed_cidrs) : toset([])

  security_group_id = aws_security_group.app[0].id
  description       = "messageServer from ${each.value}"
  cidr_ipv4         = each.value
  from_port         = var.app_port
  to_port           = var.app_port
  ip_protocol       = "tcp"
}

# No inbound SSH: the instance is reached through Session Manager, which needs
# no open port and no key pair (see the README).
resource "aws_vpc_security_group_egress_rule" "app_out" {
  count = var.deploy_app ? 1 : 0

  security_group_id = aws_security_group.app[0].id
  description       = "Outbound: S3, SSM, package updates"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# Pods get VPC addresses from the CNI and carry the cluster security group, so
# one rule covers all of them however often they are rescheduled.
resource "aws_vpc_security_group_ingress_rule" "database_from_eks" {
  count = var.deploy_eks ? 1 : 0

  security_group_id            = aws_security_group.database.id
  description                  = "Postgres from the EKS pods"
  referenced_security_group_id = aws_eks_cluster.this[0].vpc_config[0].cluster_security_group_id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
}
