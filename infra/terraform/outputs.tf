output "aws_account_id" {
  description = "The account everything was created in — check it against the console before applying."
  value       = data.aws_caller_identity.current.account_id
}

output "region" {
  description = "The region everything was created in."
  value       = data.aws_region.current.region
}

output "allowed_cidrs" {
  description = "Who can reach the database and the app."
  value       = local.allowed_cidrs
}

output "database_endpoint" {
  description = "host:port of the database."
  value       = aws_db_instance.this.endpoint
}

output "database_url" {
  description = "The line to put in .env. Read it with: terraform output -raw database_url"
  value       = local.database_url
  sensitive   = true
}

output "database_ssm_parameter" {
  description = "Parameter Store name holding the same connection string."
  value       = aws_ssm_parameter.database_url.name
}

output "psql_command" {
  description = "Connect with psql."
  value       = "psql '${local.database_url}'"
  sensitive   = true
}

output "app_public_ip" {
  description = "Public address of the server, when deploy_app is on."
  value       = var.deploy_app ? aws_instance.app[0].public_ip : null
}

output "app_url" {
  description = "The plain browser client, when deploy_app is on."
  value       = var.deploy_app ? "http://${aws_instance.app[0].public_ip}:${var.app_port}/" : null
}

output "webapp_url" {
  description = "The React client, when deploy_app is on."
  value       = var.deploy_app ? "http://${aws_instance.app[0].public_ip}:${var.app_port}/webapp" : null
}

output "app_instance_id" {
  description = "For: aws ssm start-session --target <id>"
  value       = var.deploy_app ? aws_instance.app[0].id : null
}

# ---------------------------------------------------------------------------
# EKS
# ---------------------------------------------------------------------------

output "eks_cluster_name" {
  description = "Name of the EKS cluster, when deploy_eks is on."
  value       = var.deploy_eks ? aws_eks_cluster.this[0].name : null
}

output "eks_cluster_endpoint" {
  description = "Kubernetes API server address."
  value       = var.deploy_eks ? aws_eks_cluster.this[0].endpoint : null
}

output "kubeconfig_command" {
  description = "Point kubectl at the cluster."
  value = var.deploy_eks ? join(" ", concat(
    ["aws", "eks", "update-kubeconfig", "--region", var.region, "--name", aws_eks_cluster.this[0].name],
    var.profile != "" ? ["--profile", var.profile] : [],
  )) : null
}

output "ecr_repository_url" {
  description = "Where the server's image is pushed."
  value       = var.deploy_eks && var.app_image == "" ? aws_ecr_repository.app[0].repository_url : null
}

output "app_image" {
  description = "The exact image the pods are running. The tag is a hash of the server's sources unless app_image_tag was set."
  value       = var.deploy_eks ? local.app_image : null
}

output "alb_dns_name" {
  description = "Public address of the Application Load Balancer."
  value       = var.deploy_eks ? kubernetes_ingress_v1.app[0].status[0].load_balancer[0].ingress[0].hostname : null
}

output "eks_app_url" {
  description = "The plain browser client, load balanced across the pods."
  value       = var.deploy_eks ? "${local.alb_scheme}://${kubernetes_ingress_v1.app[0].status[0].load_balancer[0].ingress[0].hostname}/" : null
}

output "eks_webapp_url" {
  description = "The React client, load balanced across the pods."
  value       = var.deploy_eks ? "${local.alb_scheme}://${kubernetes_ingress_v1.app[0].status[0].load_balancer[0].ingress[0].hostname}/webapp" : null
}

output "alb_certificate" {
  description = "Which certificate the listener presents, and whether a browser will accept it without complaint."
  value = !var.deploy_eks || !var.alb_https ? null : (
    local.alb_self_signed
    ? "self-signed (${local.alb_certificate_arn}) — every browser will warn; set alb_certificate_arn to a real ACM certificate to stop that"
    : local.alb_certificate_arn
  )
}

output "eks_pods_command" {
  description = "See the replicas."
  value       = var.deploy_eks ? "kubectl -n ${var.kubernetes_namespace} get pods -o wide" : null
}

output "acm_validation_record" {
  description = "The DNS record proving the domain is yours. Add it at your registrar, then apply again."
  value = !var.deploy_eks || var.domain_name == "" || var.alb_certificate_arn != "" ? null : {
    for option in aws_acm_certificate.app[0].domain_validation_options :
    option.domain_name => {
      type  = option.resource_record_type
      name  = option.resource_record_name
      value = option.resource_record_value
      # Namecheap's "Host" column wants the name relative to the zone, so the
      # zone's own suffix comes off. This is that, already trimmed.
      namecheap_host = trimsuffix(
        trimsuffix(option.resource_record_name, "."),
        ".${join(".", slice(split(".", var.domain_name), length(split(".", var.domain_name)) - 2, length(split(".", var.domain_name))))}"
      )
    }
  }
}

output "dns_target" {
  description = "What domain_name should point at: a CNAME to this hostname."
  value       = var.deploy_eks ? kubernetes_ingress_v1.app[0].status[0].load_balancer[0].ingress[0].hostname : null
}

output "app_url_on_domain" {
  description = "Where the app will answer once the records are in place."
  value       = var.deploy_eks && var.domain_name != "" ? "https://${var.domain_name}/" : null
}
