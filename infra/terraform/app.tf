# The server itself, on one free-tier EC2 instance.
#
# The whole application is a single static binary — the HTML templates and the
# built React app are compiled into it — so "deploy" here means: put the binary
# in S3, let the instance fetch it at boot, and run it under systemd with
# DATABASE_URL read from Parameter Store. There is nothing else to install and
# nothing to build on the instance.
#
# Everything in this file is created only when deploy_app is true.

locals {
  app_count = var.deploy_app ? 1 : 0

  # Amazon Linux 2023, whichever AMI is current in this region for the chosen
  # architecture. AWS publishes the id in a public SSM parameter.
  ami_parameter = {
    x86_64 = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64"
    arm64  = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-arm64"
  }
}

data "aws_ssm_parameter" "ami" {
  count = local.app_count
  name  = local.ami_parameter[var.app_architecture]
}

# ---------------------------------------------------------------------------
# The release artifact
# ---------------------------------------------------------------------------

resource "random_id" "bucket" {
  count       = local.app_count
  byte_length = 4
}

resource "aws_s3_bucket" "releases" {
  count = local.app_count
  # Bucket names are globally unique, so the account's name prefix alone is not
  # enough.
  bucket        = "${var.name}-releases-${random_id.bucket[0].hex}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "releases" {
  count                   = local.app_count
  bucket                  = aws_s3_bucket.releases[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "releases" {
  count  = local.app_count
  bucket = aws_s3_bucket.releases[0].id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_object" "binary" {
  count  = local.app_count
  bucket = aws_s3_bucket.releases[0].id
  key    = "messageServer"
  source = var.app_binary_path
  # Without this a rebuilt binary of the same size looks unchanged to S3.
  etag = filemd5(var.app_binary_path)
}

# ---------------------------------------------------------------------------
# What the instance is allowed to do
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "assume_ec2" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "app" {
  count              = local.app_count
  name               = "${var.name}-app"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
}

# Session Manager, so the instance can be reached for a look around without an
# SSH key or an open port 22.
resource "aws_iam_role_policy_attachment" "ssm_core" {
  count      = local.app_count
  role       = aws_iam_role.app[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "app" {
  count = local.app_count

  statement {
    sid       = "ReadTheRelease"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.releases[0].arn}/*"]
  }

  statement {
    sid       = "ReadTheConnectionString"
    actions   = ["ssm:GetParameter"]
    resources = [aws_ssm_parameter.database_url.arn]
  }
}

resource "aws_iam_role_policy" "app" {
  count  = local.app_count
  name   = "${var.name}-app"
  role   = aws_iam_role.app[0].id
  policy = data.aws_iam_policy_document.app[0].json
}

resource "aws_iam_instance_profile" "app" {
  count = local.app_count
  name  = "${var.name}-app"
  role  = aws_iam_role.app[0].name
}

# ---------------------------------------------------------------------------
# The instance
# ---------------------------------------------------------------------------

resource "aws_instance" "app" {
  count = local.app_count

  ami                    = data.aws_ssm_parameter.ami[0].value
  instance_type          = var.app_instance_type
  subnet_id              = data.aws_subnets.default.ids[0]
  vpc_security_group_ids = [aws_security_group.app[0].id]
  iam_instance_profile   = aws_iam_instance_profile.app[0].name

  user_data = templatefile("${path.module}/user_data.sh.tftpl", {
    region    = data.aws_region.current.region
    bucket    = aws_s3_bucket.releases[0].id
    key       = aws_s3_object.binary[0].key
    parameter = aws_ssm_parameter.database_url.name
    port      = var.app_port
    # Not used by the script: it is here so that a new binary changes the user
    # data, which replaces the instance and so actually deploys the new build.
    release = aws_s3_object.binary[0].etag
  })
  user_data_replace_on_change = true

  metadata_options {
    # IMDSv2 only: the instance's credentials should not be one unauthenticated
    # HTTP request away for anything that can reach it.
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_size = 8
    volume_type = "gp3"
    encrypted   = true
  }

  tags = {
    Name = "${var.name}-app"
  }

  # The server applies pending migrations at startup, so it needs the database
  # to exist first.
  depends_on = [aws_db_instance.this]
}
