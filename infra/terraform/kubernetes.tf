# The workload: eks_replicas copies of messageServer, behind one ALB.
#
# The replicas are interchangeable. Everything a request depends on — the
# authorized keys, usernames, chat membership, and since REQ-009 the message log
# itself — lives in the Postgres instance next door, so any pod can serve any
# client and the load balancer is free to send a request wherever it likes.
#
# That was not true when this file was first written: messages were held in a
# slice inside each process, and two clients on different pods could not see
# each other. messages.go is what changed; this file only stopped having to work
# around it.

locals {
  app_labels = {
    "app.kubernetes.io/name"       = "messageserver"
    "app.kubernetes.io/component"  = "server"
    "app.kubernetes.io/managed-by" = "terraform"
  }
}

resource "kubernetes_namespace_v1" "app" {
  count = local.eks_count

  metadata {
    name   = var.kubernetes_namespace
    labels = local.app_labels
  }

  depends_on = [aws_eks_node_group.this]
}

# The same connection string the EC2 route reads from Parameter Store. It is in
# a Secret because that is what a pod can mount as an environment variable; it
# is not more secret than the Terraform state, which has held the password in
# clear text since the database was created.
resource "kubernetes_secret_v1" "database" {
  count = local.eks_count

  metadata {
    name      = "messageserver-database"
    namespace = kubernetes_namespace_v1.app[0].metadata[0].name
    labels    = local.app_labels
  }

  data = {
    DATABASE_URL = local.database_url
  }

  type = "Opaque"
}

# ---------------------------------------------------------------------------
# Schema
# ---------------------------------------------------------------------------
#
# Migrations run once, here, before any pod starts. The server would happily
# apply them itself — that is its default — but three pods starting at the same
# moment would race, and Apply has no advisory lock: two of them would fail on
# the duplicate CREATE TABLE, exit, and be restarted by Kubernetes until the
# winner had finished. It works, and it looks like a crash loop while it does.
#
# The Job's name carries the image tag because a Job's pod template is
# immutable: a new build needs a new Job rather than an edit to this one.
resource "kubernetes_job_v1" "migrate" {
  count = local.eks_count

  metadata {
    name      = "messageserver-migrate-${local.image_tag}"
    namespace = kubernetes_namespace_v1.app[0].metadata[0].name
    labels    = local.app_labels
  }

  spec {
    backoff_limit = 3

    template {
      metadata {
        labels = local.app_labels
      }

      spec {
        restart_policy = "OnFailure"

        container {
          name    = "migrate"
          image   = local.app_image
          command = ["/usr/local/bin/migrate"]

          env {
            name = "DATABASE_URL"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.database[0].metadata[0].name
                key  = "DATABASE_URL"
              }
            }
          }

          resources {
            requests = {
              cpu    = "50m"
              memory = "64Mi"
            }
            limits = {
              memory = "128Mi"
            }
          }
        }
      }
    }
  }

  wait_for_completion = true

  timeouts {
    create = "10m"
    update = "10m"
  }

  depends_on = [
    aws_db_instance.this,
    aws_vpc_security_group_ingress_rule.database_from_eks,
    terraform_data.image,
    aws_eks_addon.coredns,
  ]
}

# ---------------------------------------------------------------------------
# The pods
# ---------------------------------------------------------------------------

resource "kubernetes_deployment_v1" "app" {
  count = local.eks_count

  metadata {
    name      = "messageserver"
    namespace = kubernetes_namespace_v1.app[0].metadata[0].name
    labels    = local.app_labels
  }

  spec {
    replicas = var.eks_replicas

    selector {
      match_labels = local.app_labels
    }

    strategy {
      type = "RollingUpdate"
      rolling_update {
        max_surge       = 1
        max_unavailable = 0
      }
    }

    template {
      metadata {
        labels = local.app_labels
      }

      spec {
        # Spread the replicas over availability zones rather than stacking them
        # on whichever node has room. ScheduleAnyway, because with three pods on
        # two nodes a perfectly even spread is not possible.
        topology_spread_constraint {
          max_skew           = 1
          topology_key       = "topology.kubernetes.io/zone"
          when_unsatisfiable = "ScheduleAnyway"
          label_selector {
            match_labels = local.app_labels
          }
        }

        container {
          name  = "messageserver"
          image = local.app_image

          # -require-schema: refuse to start against an out-of-date database
          # rather than migrating, because the Job above owns that.
          args = ["-addr=:${var.app_port}", "-require-schema"]

          env {
            name = "DATABASE_URL"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.database[0].metadata[0].name
                key  = "DATABASE_URL"
              }
            }
          }

          port {
            name           = "http"
            container_port = var.app_port
          }

          # "/" is the plain browser client, and the only route that answers
          # without a signed request: every /api path returns 401 to an
          # unauthenticated caller, which a health check would read as failure.
          readiness_probe {
            http_get {
              path = "/"
              port = var.app_port
            }
            initial_delay_seconds = 5
            period_seconds        = 10
            failure_threshold     = 3
          }

          liveness_probe {
            http_get {
              path = "/"
              port = var.app_port
            }
            initial_delay_seconds = 15
            period_seconds        = 20
            failure_threshold     = 3
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "64Mi"
            }
            limits = {
              memory = "256Mi"
            }
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities {
              drop = ["ALL"]
            }
          }
        }

        security_context {
          run_as_non_root = true
          run_as_user     = 10001
        }
      }
    }
  }

  # The rollout is not finished until the pods are actually serving, which is
  # what makes the ALB's targets healthy by the time apply returns.
  wait_for_rollout = true

  timeouts {
    create = "10m"
    update = "10m"
  }

  depends_on = [kubernetes_job_v1.migrate]
}

resource "kubernetes_service_v1" "app" {
  count = local.eks_count

  metadata {
    name      = "messageserver"
    namespace = kubernetes_namespace_v1.app[0].metadata[0].name
    labels    = local.app_labels
  }

  spec {
    # ClusterIP is enough: with target-type "ip" the ALB sends traffic straight
    # to pod addresses and uses the Service only to find them.
    type     = "ClusterIP"
    selector = local.app_labels

    port {
      name        = "http"
      port        = 80
      target_port = var.app_port
      protocol    = "TCP"
    }
  }
}

# ---------------------------------------------------------------------------
# The load balancer
# ---------------------------------------------------------------------------

resource "kubernetes_ingress_v1" "app" {
  count = local.eks_count

  metadata {
    name      = "messageserver"
    namespace = kubernetes_namespace_v1.app[0].metadata[0].name
    labels    = local.app_labels

    annotations = {
      "alb.ingress.kubernetes.io/scheme"           = "internet-facing"
      "alb.ingress.kubernetes.io/target-type"      = "ip"
      "alb.ingress.kubernetes.io/listen-ports"     = jsonencode([{ HTTP = 80 }])
      "alb.ingress.kubernetes.io/healthcheck-path" = "/"
      "alb.ingress.kubernetes.io/success-codes"    = "200"

      # The same "only this machine" default as the database's security group.
      "alb.ingress.kubernetes.io/inbound-cidrs" = join(",", local.allowed_cidrs)

      # No stickiness: it was here only to pin a client to the pod holding its
      # messages, and the message log is in the database now. Requests are free
      # to land anywhere, which is what makes losing a pod uneventful.
      #
      # The deregistration delay is unrelated and stays: it is how long a pod
      # being replaced is given to finish the requests already in flight.
      "alb.ingress.kubernetes.io/target-group-attributes" = "deregistration_delay.timeout_seconds=30"
    }
  }

  spec {
    ingress_class_name = "alb"

    rule {
      http {
        # One rule for everything: "/" and "/webapp" are both served by the same
        # binary, so there is nothing to route between.
        path {
          path      = "/"
          path_type = "Prefix"

          backend {
            service {
              name = kubernetes_service_v1.app[0].metadata[0].name
              port {
                number = 80
              }
            }
          }
        }
      }
    }
  }

  # So that alb_dns_name is populated by the time apply finishes, rather than
  # being empty until the next refresh.
  wait_for_load_balancer = true

  timeouts {
    create = "15m"
  }

  depends_on = [
    helm_release.alb_controller,
    kubernetes_deployment_v1.app,
  ]
}
