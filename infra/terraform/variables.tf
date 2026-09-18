variable "region" {
  description = "AWS region to create everything in."
  type        = string
  default     = "us-east-1"
}

variable "profile" {
  description = "Named AWS CLI profile to use. Empty means the default credential chain (AWS_PROFILE, SSO, environment)."
  type        = string
  default     = ""
}

variable "name" {
  description = "Name prefix for every resource, so they are recognisable in the console."
  type        = string
  default     = "gomessageserver"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,24}$", var.name))
    error_message = "name must be 2-25 characters of lower-case letters, digits and hyphens, starting with a letter."
  }
}

variable "tags" {
  description = "Extra tags applied to every resource."
  type        = map(string)
  default     = {}
}

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

variable "postgres_version" {
  description = "PostgreSQL major version. RDS picks the current minor release within it."
  type        = string
  default     = "17"
}

variable "db_instance_class" {
  description = "RDS instance class. db.t4g.micro and db.t3.micro are the free-tier eligible ones."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_allocated_storage" {
  description = "Storage in GiB. The free tier covers 20."
  type        = number
  default     = 20
}

variable "db_name" {
  description = "Name of the database created inside the instance."
  type        = string
  default     = "messages"
}

variable "db_username" {
  description = "Master user name. RDS reserves 'admin', 'rdsadmin' and a few others."
  type        = string
  default     = "messages"
}

variable "db_backup_retention_days" {
  description = "Days of automated backups to keep. 0 turns them off; the free tier covers backup storage up to the size of the database."
  type        = number
  default     = 7
}

variable "db_publicly_accessible" {
  description = "Give the database a public address, so you can run migrations and psql from your laptop. It is still only reachable from allowed_cidrs."
  type        = bool
  default     = true
}

variable "db_deletion_protection" {
  description = "Refuse to destroy the database. Worth turning on once it holds anything you care about."
  type        = bool
  default     = false
}

variable "allowed_cidrs" {
  description = "CIDR blocks allowed to reach the database and the app. Empty means just this machine's current public IP, looked up at plan time."
  type        = list(string)
  default     = []
}

# ---------------------------------------------------------------------------
# The application
# ---------------------------------------------------------------------------

variable "deploy_app" {
  description = "Also run messageServer on a free-tier EC2 instance. Needs app_binary_path."
  type        = bool
  default     = false
}

variable "app_binary_path" {
  description = "Path to a Linux build of the server (see the README: GOOS=linux go build). Uploaded to S3 and fetched by the instance at boot."
  type        = string
  default     = ""

  validation {
    condition     = !var.deploy_app || var.app_binary_path != ""
    error_message = "deploy_app needs app_binary_path: build the server for Linux first, e.g. GOOS=linux GOARCH=amd64 go build -o messageServer-linux-amd64 ./cmd/messageServer"
  }
}

variable "app_instance_type" {
  description = "EC2 instance type. t3.micro (x86_64) and t2.micro are the free-tier eligible ones in most regions."
  type        = string
  default     = "t3.micro"
}

variable "app_architecture" {
  description = "CPU architecture of app_binary_path and of the AMI. Must match app_instance_type: t3/t2 are x86_64, t4g is arm64."
  type        = string
  default     = "x86_64"

  validation {
    condition     = contains(["x86_64", "arm64"], var.app_architecture)
    error_message = "app_architecture must be x86_64 or arm64."
  }
}

variable "app_port" {
  description = "Port the server listens on, and the one opened to allowed_cidrs."
  type        = number
  default     = 8080
}

# ---------------------------------------------------------------------------
# EKS: the same server, as three pods behind an ALB
# ---------------------------------------------------------------------------

variable "deploy_eks" {
  description = "Create an EKS cluster running the server as replicas behind an ALB. Independent of deploy_app, which is the single-EC2-instance route. NOT free tier: the control plane alone is about $0.10/hour."
  type        = bool
  default     = false
}

variable "eks_version" {
  description = "Kubernetes minor version, e.g. \"1.33\". Empty means whatever EKS currently defaults to, which is the safer choice."
  type        = string
  default     = ""
}

variable "eks_replicas" {
  description = "How many messageServer pods to run. REQ-008 asks for three."
  type        = number
  default     = 3

  validation {
    condition     = var.eks_replicas >= 1
    error_message = "eks_replicas must be at least 1."
  }
}

variable "eks_node_instance_type" {
  description = "Instance type for the managed node group. t3.micro is too small: its ENI limit caps it at 4 pods, before CoreDNS and the ALB controller take theirs."
  type        = string
  default     = "t3.small"
}

variable "eks_node_desired_count" {
  description = "Nodes in the managed node group. Two is enough to spread eks_replicas across availability zones."
  type        = number
  default     = 2
}

variable "eks_node_min_count" {
  description = "Lower bound for the node group."
  type        = number
  default     = 1
}

variable "eks_node_max_count" {
  description = "Upper bound for the node group."
  type        = number
  default     = 3
}

variable "eks_excluded_azs" {
  description = <<-EOT
    Availability zones to keep the cluster out of. EKS refuses to put a control
    plane in some of them and offers no API that says which, so the list is
    maintained by hand; us-east-1e is the long-standing one. Naming a zone that
    does not exist in your region is harmless, which is why the default is safe
    to leave alone outside us-east-1.
  EOT
  type        = list(string)
  default     = ["us-east-1e"]
}

variable "eks_public_access_cidrs" {
  description = "Who may reach the Kubernetes API server. Empty falls back to allowed_cidrs, i.e. this machine. Widen it if you apply from CI as well, or you will lock Terraform out."
  type        = list(string)
  default     = []
}

variable "build_image" {
  description = "Build the image from the Dockerfile at the repository root and push it to ECR during apply. Needs docker and a working aws CLI. Turn it off to push by hand, or to point at an image somebody else built."
  type        = bool
  default     = true
}

variable "app_image" {
  description = "Full image reference to run instead of building one, e.g. \"123456789012.dkr.ecr.us-east-1.amazonaws.com/gomessageserver:abc123\". Empty means use the ECR repository this configuration creates."
  type        = string
  default     = ""
}

variable "app_image_tag" {
  description = "Tag to build and run. Empty means a hash of the server's sources, so a code change produces a new tag and therefore an actual rollout."
  type        = string
  default     = ""
}

variable "alb_controller_chart_version" {
  description = "Version of the aws-load-balancer-controller Helm chart. Empty takes the latest, which is convenient but not reproducible — pin it once you have an apply that works."
  type        = string
  default     = ""
}

variable "kubernetes_namespace" {
  description = "Namespace the server runs in."
  type        = string
  default     = "messageserver"
}

# ---------------------------------------------------------------------------
# TLS on the load balancer
# ---------------------------------------------------------------------------

variable "alb_https" {
  description = "Serve over HTTPS. Nothing is served over plain HTTP: port 80 either redirects to 443 or is not opened at all. Needed for more than tidiness — the browser clients sign every request with WebCrypto, and crypto.subtle only exists in a secure context."
  type        = bool
  default     = true
}

variable "alb_certificate_arn" {
  description = "ACM certificate for the listener. Empty means generate a self-signed one and import it, which works everywhere and is trusted nowhere: every browser will interrupt with a warning. Point this at a real certificate for a domain you own to make that stop."
  type        = string
  default     = ""
}

variable "alb_http_redirect" {
  description = "Keep port 80 open for the sole purpose of redirecting to 443. Turning it off means an http:// URL fails to connect rather than being corrected, which is stricter and less forgiving."
  type        = bool
  default     = true
}

variable "alb_ssl_policy" {
  description = "ELB security policy — which TLS versions and ciphers the listener accepts. The default allows TLS 1.2 and 1.3 only."
  type        = string
  default     = "ELBSecurityPolicy-TLS13-1-2-2021-06"
}


variable "domain_name" {
  description = "Fully qualified name to serve the app on, e.g. \"messages.example.com\". Setting it requests an ACM certificate for that name; the DNS records that validate it and point it here are yours to add at your registrar, and the outputs say exactly what they are. Empty keeps the self-signed certificate."
  type        = string
  default     = ""
}
