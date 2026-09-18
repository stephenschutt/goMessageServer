# The certificate the load balancer presents.
#
# There are two ways to have one, and the difference is entirely about trust
# rather than about encryption:
#
#   - alb_certificate_arn names a certificate you already have in ACM, issued
#     for a domain you control. Browsers accept it silently. This is the real
#     answer, and it needs a domain.
#
#   - Left empty, the certificate below is generated here and imported into
#     ACM. The connection is encrypted exactly as well, but nothing has vouched
#     for who is on the other end, so every browser stops and warns before
#     letting anyone through.
#
# The second one exists because of a specific problem it solves: the React and
# plain browser clients sign each API request with WebCrypto, and crypto.subtle
# is undefined outside a secure context. Plain HTTP is not one; HTTPS is, even
# when the certificate is untrusted and the warning has been clicked through.
# So a self-signed certificate is what makes the browser clients work at all
# over a bare ELB hostname, which cannot have a real certificate issued for it.

locals {
  alb_tls_count = var.deploy_eks && var.alb_https && var.alb_certificate_arn == "" ? 1 : 0

  # Preference order: one you named, then one ACM issued for domain_name, then
  # the self-signed fallback. The middle case only becomes available after the
  # validation record exists, which is why the self-signed certificate is still
  # created and still there to fall back to.
  alb_certificate_arn = (
    var.alb_certificate_arn != "" ? var.alb_certificate_arn :
    var.domain_name != "" ? aws_acm_certificate_validation.app[0].certificate_arn :
    local.alb_tls_count > 0 ? aws_acm_certificate.alb_self_signed[0].arn : ""
  )

  alb_self_signed = var.alb_https && var.alb_certificate_arn == "" && var.domain_name == ""
  alb_scheme      = var.alb_https ? "https" : "http"
}

resource "tls_private_key" "alb" {
  count     = local.alb_tls_count
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "alb" {
  count           = local.alb_tls_count
  private_key_pem = tls_private_key.alb[0].private_key_pem

  subject {
    common_name  = "${var.name}.invalid"
    organization = var.name
  }

  # The load balancer's hostname is not known until it exists, and this
  # certificate has to exist first — so it claims the whole of the ELB domain.
  # No certificate authority would issue that, which is precisely why this one
  # is signed by nobody.
  dns_names = ["*.elb.amazonaws.com", "${var.name}.invalid", "localhost"]

  validity_period_hours = 8760 # a year
  early_renewal_hours   = 720  # replaced a month before it lapses

  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "server_auth",
  ]
}

resource "aws_acm_certificate" "alb_self_signed" {
  count = local.alb_tls_count

  private_key      = tls_private_key.alb[0].private_key_pem
  certificate_body = tls_self_signed_cert.alb[0].cert_pem

  tags = {
    Name = "${var.name}-alb-self-signed"
  }

  lifecycle {
    # The listener references this certificate, so a replacement has to exist
    # before the old one can go.
    create_before_destroy = true
  }
}

# ---------------------------------------------------------------------------
# A real certificate, for a domain whose DNS lives somewhere else
# ---------------------------------------------------------------------------
#
# ACM issues the certificate; proving the domain is yours is done by publishing
# a record it names. When the zone is in Route 53 that can be automated, and
# when it is at another registrar it cannot — so this half stops at "here is
# the record to add", and the two outputs in outputs.tf say what to add and
# where to point it.
#
# The order is: apply once to request the certificate, add the record it asks
# for, then apply again. The second apply is when aws_acm_certificate_validation
# stops waiting and the listener swaps the self-signed certificate for this one.

resource "aws_acm_certificate" "app" {
  count = var.deploy_eks && var.domain_name != "" && var.alb_certificate_arn == "" ? 1 : 0

  domain_name       = var.domain_name
  validation_method = "DNS"

  tags = {
    Name = "${var.name}-alb"
  }

  lifecycle {
    create_before_destroy = true
  }
}

# Blocks until the record below has been published and ACM has seen it. If an
# apply sits here for a long time, the record is missing or wrong — check it
# with: dig +short CNAME <the name from acm_validation_record>
resource "aws_acm_certificate_validation" "app" {
  count = length(aws_acm_certificate.app)

  certificate_arn = aws_acm_certificate.app[0].arn

  timeouts {
    create = "20m"
  }
}
