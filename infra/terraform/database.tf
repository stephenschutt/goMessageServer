# The free-tier PostgreSQL instance.
#
# What keeps it inside the free tier: a db.t4g.micro (or db.t3.micro) class,
# 20 GiB of gp2 storage, Single-AZ, and no Performance Insights or enhanced
# monitoring. The 750 instance-hours a month that covers it are a 12-month
# benefit on new accounts — check your Billing console before leaving it up.

resource "random_password" "db" {
  length = 32
  # Alphanumeric only: the password goes into a postgres:// URL, and percent
  # encoding a password is exactly the sort of thing that silently half-works.
  special = false
}

resource "aws_db_subnet_group" "this" {
  name        = "${var.name}-db"
  description = "Default VPC subnets for ${var.name}"
  subnet_ids  = data.aws_subnets.default.ids
}

resource "aws_db_instance" "this" {
  identifier = "${var.name}-db"

  engine         = "postgres"
  engine_version = var.postgres_version
  instance_class = var.db_instance_class

  db_name  = var.db_name
  username = var.db_username
  password = random_password.db.result

  allocated_storage = var.db_allocated_storage
  storage_type      = "gp2"
  # Encryption at rest is free with the default RDS key, and it cannot be turned
  # on later without rebuilding the instance.
  storage_encrypted = true

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.database.id]
  publicly_accessible    = var.db_publicly_accessible
  multi_az               = false

  backup_retention_period    = var.db_backup_retention_days
  auto_minor_version_upgrade = true
  # Free tier does not cover these two, and they are on by default in some
  # regions.
  performance_insights_enabled = false
  monitoring_interval          = 0

  deletion_protection = var.db_deletion_protection
  # A development database: `terraform destroy` should actually destroy it
  # rather than stopping to ask for a snapshot name.
  skip_final_snapshot = !var.db_deletion_protection
  apply_immediately   = true

  lifecycle {
    # RDS reports the running minor version here, which would otherwise show up
    # as a diff against the major version pinned in the variable.
    ignore_changes = [engine_version]
  }
}

# The connection string in the exact spelling DATABASE_URL wants, kept as a
# SecureString so the instance can read it at boot without the password passing
# through user data (which is readable from the instance metadata service).
# Standard parameters are free.
resource "aws_ssm_parameter" "database_url" {
  name        = "/${var.name}/DATABASE_URL"
  description = "messageServer connection string"
  type        = "SecureString"
  value       = local.database_url
}

locals {
  database_url = format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    var.db_username,
    random_password.db.result,
    aws_db_instance.this.address,
    aws_db_instance.this.port,
    var.db_name,
  )
}
