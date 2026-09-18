# goMessageServer — infrastructure

Terraform for a free-tier PostgreSQL database on AWS — the one `DATABASE_URL`
in `.env` points at — and, optionally, a free-tier EC2 instance running the
server itself.

## What it creates

| | |
| --- | --- |
| `aws_db_instance` | PostgreSQL 17 on `db.t4g.micro`, 20 GiB gp2, Single-AZ, encrypted |
| `aws_db_subnet_group`, `aws_security_group` | in the account's **default VPC**, reachable only from `allowed_cidrs` |
| `random_password` | the master password — generated, never typed, never in this repo |
| `aws_ssm_parameter` | the connection string as a `SecureString` (Parameter Store standard tier is free) |
| *with `deploy_app = true`* | S3 bucket for the built binary, IAM role, security group, and one `t3.micro` running the server under systemd |

There is deliberately no NAT gateway, no Multi-AZ, no Performance Insights and
no enhanced monitoring: those are the parts of a "normal" setup that cost real
money. The default VPC's subnets are public and free, which is why the database
sits there behind a security group rather than in private subnets.

**Free tier is a 12-month benefit on new accounts** (750 db.t4g/t3.micro hours,
20 GiB storage, 750 t3.micro EC2 hours a month). On an older account this is
cheap, not free — check the Billing console before leaving it running.

## Before you start

- Terraform ≥ 1.9 — `brew install terraform`
- The AWS CLI signed in to the account you want this in: `aws configure`, or
  `aws sso login --profile <name>`. Terraform uses the same credentials.

## The database

```sh
cd infra/terraform
cp terraform.tfvars.example terraform.tfvars   # optional; every value has a default
terraform init
terraform plan                                  # check aws_account_id in the output first
terraform apply
```

Then point the app at it:

```sh
terraform output -raw database_url              # postgres://messages:...@...:5432/messages?sslmode=require
```

Put that in `.env` at the repo root as `DATABASE_URL=...`, then create the
tables and start the server:

```sh
cd ../..                # back to the repository root
go run ./migrate        # optional: the server applies pending migrations itself
go run ./cmd/messageServer
```

`terraform output -raw psql_command` prints a ready-to-paste `psql` line for
looking at the data by hand.

By default only **this machine's current public IP** is allowed to connect: the
address is looked up when you plan, so a new coffee shop means
`terraform apply` again, or set `allowed_cidrs` to something stable.

## Deploying the server as well

The whole application is one static binary — the HTML templates and the built
React app are compiled into it — so deploying it is: build for Linux, upload,
run. Build the webapp first if you have changed it.

```sh
# from the repository root
(cd webapp && npm install && npm run build)
GOOS=linux GOARCH=amd64 go build -o messageServer-linux-amd64 ./cmd/messageServer

cd infra/terraform
terraform apply -var deploy_app=true -var app_binary_path=../../messageServer-linux-amd64
```

Terraform uploads the binary to S3; the instance fetches it at boot, reads the
connection string from Parameter Store, and runs it under systemd. Outputs give
you the two clients:

```sh
terraform output app_url        # http://<ip>:8080/       the plain browser client
terraform output webapp_url     # http://<ip>:8080/webapp  the React client
```

To deploy a new build, rebuild the binary and apply again: the object's checksum
is part of the instance's user data, so a changed binary replaces the instance.

Neither client can do anything until its key is authorized, exactly as on a
laptop. There is no SSH key and no open port 22 — use Session Manager:

```sh
aws ssm start-session --target "$(terraform output -raw app_instance_id)"
# on the instance:
sudo journalctl -u messageserver -f          # the server's log
sudo cat /var/log/messageserver-setup.log    # what the boot script did
sudo /opt/messageserver/messageServer -env /etc/messageserver.env -pending
sudo /opt/messageserver/messageServer -env /etc/messageserver.env -authorize '<key>'
```

The instance is plain HTTP on port 8080, so the React client's WebCrypto signing
will refuse to run on it from a browser (it needs a secure context) — the iOS
app is fine, and so is the browser over an SSH-style tunnel to localhost. Put it
behind a TLS terminator before treating it as anything but a demo.

## Variables worth knowing

| Variable | Default | Why you would change it |
| --- | --- | --- |
| `region` | `us-east-1` | somewhere nearer |
| `profile` | `""` | pick between several AWS accounts |
| `name` | `gomessageserver` | name prefix on every resource |
| `allowed_cidrs` | *this machine's IP* | a stable office or home range |
| `db_instance_class` | `db.t4g.micro` | `db.t3.micro` if t4g is unavailable in the region |
| `postgres_version` | `17` | pin a different major version |
| `db_deletion_protection` | `false` | once it holds anything you care about |
| `deploy_app` | `false` | also run the server on EC2 |
| `app_binary_path` | `""` | required when `deploy_app` is on |
| `app_instance_type` / `app_architecture` | `t3.micro` / `x86_64` | `t4g.micro` / `arm64`, with a matching `GOARCH=arm64` build |

To check what a region actually offers before pinning a version or class:

```sh
aws rds describe-db-engine-versions --engine postgres \
  --query 'DBEngineVersions[].EngineVersion' --output text
aws rds describe-orderable-db-instance-options --engine postgres \
  --engine-version 17 --query 'OrderableDBInstanceOptions[].DBInstanceClass' \
  --output text | tr '\t' '\n' | sort -u | head
```

## Running it on EKS instead: three pods behind an ALB

> **This part is not free tier.** The EKS control plane is billed at about
> $0.10/hour whether or not anything runs on it (~$73/month), the ALB adds
> ~$16/month, and the two `t3.small` nodes are ordinary on-demand EC2 (~$30/month).
> Call it **$90-100 a month**, starting the moment you apply. The database next
> door stays free. `terraform destroy` is the off switch, and there is no
> partial one.

`deploy_eks = true` builds the server into a container image, pushes it to ECR,
and runs `eks_replicas` (default 3) copies of it on a managed node group, load
balanced by an Application Load Balancer. It is independent of `deploy_app`:
that one is the single-EC2-instance route, and you do not need both.

### Redeploying from scratch

After a `terraform destroy`, the whole thing comes back with this. No `-var`
flags: `deploy_eks` and `domain_name` live in `infra/terraform/terraform.tfvars`.

```sh
cd infra/terraform

# 1. Request the certificate (returns in seconds)
terraform apply -target=aws_acm_certificate.app
terraform output acm_validation_record

# 2. Update the validation CNAME at the registrar — a rebuild gets a NEW
#    value, so the previous record is dead and must be replaced, not reused.

# 3. Build everything (~25 minutes; step 4 blocks until step 2 is visible)
terraform apply
terraform output dns_target

# 4. Update the app's CNAME at the registrar — the new ALB has a NEW hostname.

# 5. Connect, and re-authorize each client
$(terraform output -raw kubeconfig_command)
pod=$(kubectl -n messageserver get po -l app.kubernetes.io/name=messageserver -o name | head -1)
kubectl -n messageserver exec "$pod" -- messageServer -pending
kubectl -n messageserver exec "$pod" -- messageServer -authorize '<key>'
```

Three things a rebuild does not preserve, all for the same reason — they were
destroyed, not stopped:

- **The database is empty.** `skip_final_snapshot` means no snapshot to restore,
  so every authorized key, username and message is gone. `pg_dump` before a
  destroy if you want any of it.
- **Both DNS records change.** New certificate, new validation value; new load
  balancer, new hostname.
- **The database password changes.** `random_password` regenerates, so a saved
  client such as DBeaver will fail to authenticate until you re-read
  `terraform output -raw database_url`.

Skip steps 1, 2 and 4 by commenting out `domain_name`, which falls back to the
self-signed certificate: `terraform apply` then does the whole job, at the cost
of a browser warning.

To redeploy only a **code change** with the cluster already up, `terraform apply`
is the whole thing — the image tag is a hash of the server's sources, so a
changed file is a new tag, a new image and a rolling update.

### Before you start

On top of Terraform and the AWS CLI:

- **Docker, running.** The image is built during apply.
- **A working `aws` CLI.** The Kubernetes provider authenticates with
  `aws eks get-token`, and the image push uses `aws ecr get-login-password`.
- `kubectl`, to look at the cluster afterwards — `brew install kubectl`.

### Deploying

Build the React app first if you have changed it; it is compiled into the
binary, which is compiled into the image.

```sh
# from the repository root
(cd webapp && npm install && npm run build)

cd infra/terraform
terraform apply
```

One apply does the whole thing, in this order: cluster, node group, add-ons,
ECR repository, `docker build`/`docker push`, the load balancer controller, a
migration Job, the Deployment, and finally the Ingress that becomes the ALB.
Budget **20-25 minutes** the first time; most of it is the control plane and
the load balancer provisioning.

```sh
terraform output eks_app_url      # http://<alb>.us-east-1.elb.amazonaws.com/
terraform output eks_webapp_url   # http://<alb>.../webapp
```

### Looking at it

```sh
$(terraform output -raw kubeconfig_command)   # aws eks update-kubeconfig ...

kubectl -n messageserver get pods -o wide     # the three replicas, and their nodes
kubectl -n messageserver logs -l app.kubernetes.io/name=messageserver -f --max-log-requests=3
kubectl -n messageserver get ingress          # the ALB the controller created
```

Authorizing a key works as it does everywhere else, except that you pick a pod
to run it in. The allow list is in Postgres, so it does not matter which:

```sh
pod=$(kubectl -n messageserver get pod -l app.kubernetes.io/name=messageserver -o name | head -1)
kubectl -n messageserver exec "$pod" -- messageServer -pending
kubectl -n messageserver exec "$pod" -- messageServer -authorize '<key>'
```

### Deploying a new build

The image tag is a hash of the server's sources, so rebuilding is just applying
again: a changed file means a new tag, which means a new image and a rolling
update. Unchanged sources mean no build and no rollout.

```sh
terraform apply -var deploy_eks=true
```

### What it actually does

All three pods serve the same chat. The authorized-key list, usernames, chat
membership and — since REQ-009 — the message log are all tables in the Postgres
instance next door, so a message sent through one pod is readable through any of
them and it does not matter which one the load balancer picks.

This was not true when the EKS work first landed: `ChatStore` held messages in a
slice inside each process, so the three pods had three separate transcripts and
two clients that landed on different pods could not see each other. The ALB was
configured with cookie stickiness to paper over it. `messages.go` moved the log
into the database, and the stickiness has been removed along with the reason for
it.

One consequence worth knowing: messages now outlive the pods. A restart used to
empty the chat; it no longer does, and nothing prunes `"Messages"` — so a
long-lived deployment grows a transcript that will eventually want a retention
policy.

Three smaller notes on how it is wired:

- **Migrations run once, in a Job, before any pod starts.** The pods then run
  with `-require-schema`, which makes them refuse to start against an
  out-of-date database rather than migrating it. Letting three pods migrate
  concurrently does converge, but by way of two of them crash-looping until the
  winner commits.
- **Losing a pod loses nothing.** A replica holds no state, so Kubernetes can
  reschedule one mid-conversation and the client reconnects to another and
  carries on from the same `?since=<id>` cursor.
- **Only your IP can reach it.** Both the Kubernetes API endpoint and the ALB
  are restricted to `allowed_cidrs`, which defaults to this machine's current
  public address — the same default as the database. Set `allowed_cidrs`, or
  `eks_public_access_cidrs` for just the API, to something stable if you move
  around.

### HTTPS

`alb_https` is on by default, and nothing is served over plain HTTP: port 80
exists only to answer with a 301 to 443, and `alb_http_redirect = false` closes
it altogether.

This is not decoration. Both browser clients sign every API request with
WebCrypto, and `crypto.subtle` is undefined outside a *secure context* — which
plain HTTP is not. Over HTTP the clients load and then fail at the first signed
request. HTTPS is what makes them work.

The certificate is the awkward part. A certificate authority will only issue for
a domain you control, and `k8s-…elb.amazonaws.com` is not one. So there are two
modes:

| `alb_certificate_arn` | What happens |
| --- | --- |
| *empty* (default) | A self-signed certificate is generated in `infra/terraform/tls.tf` and imported into ACM. Encryption is real, trust is not: every browser interrupts with a warning you have to click through. WebCrypto works afterwards — a bypassed certificate error still leaves an `https://` origin, which is a secure context. |
| an ACM ARN | That certificate is used and browsers stay quiet. This is the real answer, and it needs a domain. |

#### Using your own domain

Set `domain_name` and Terraform requests the certificate; publishing the two DNS
records is yours to do, because the zone is at a registrar Terraform has no
credentials for. Route 53 could be automated, an external registrar cannot.

```sh
# 1. Request the certificate. -target keeps this to the one resource, so the
#    apply returns immediately instead of waiting on a record you cannot add yet.
terraform apply -target=aws_acm_certificate.app

# 2. Read the record ACM wants.
terraform output acm_validation_record
terraform output dns_target
```

Add both at the registrar, as CNAMEs:

| Host | Value | Why |
| --- | --- | --- |
| the `namecheap_host` from `acm_validation_record` | its `value` | proves the domain is yours, and must stay: ACM re-checks it at renewal |
| the first label of `domain_name` | `dns_target` | sends traffic to the load balancer |

```sh
# 3. Finish. aws_acm_certificate_validation waits for ACM to see the record,
#    then the listener swaps the self-signed certificate for the real one.
terraform apply
```

If step 3 sits there, the validation record is missing or wrong:

```sh
dig +short CNAME <the name from acm_validation_record>
```

Certificates must live in the same region as the load balancer, which for this
configuration means `us-east-1`.

Already have a certificate in ACM? Skip all of the above and set
`alb_certificate_arn` to its ARN.

`terraform output alb_certificate` says which of the two you are on.

To avoid the warning entirely without a domain, skip the ALB and tunnel to a
pod — `localhost` is a secure context by definition:

```sh
kubectl -n messageserver port-forward deploy/messageserver 8080:8080
# http://localhost:8080/webapp
```

The listener accepts TLS 1.2 and 1.3 only (`alb_ssl_policy`). TLS terminates at
the load balancer; the hop from there to a pod stays HTTP, inside the VPC and
inside the cluster security group.

### EKS variables

| Variable | Default | Why you would change it |
| --- | --- | --- |
| `deploy_eks` | `false` | the whole section |
| `eks_replicas` | `3` | more or fewer pods |
| `eks_node_instance_type` | `t3.small` | `t3.micro` will not do: its ENI limit caps it at 4 pods |
| `eks_node_desired_count` | `2` | more room to spread replicas |
| `eks_version` | *EKS default* | pin a Kubernetes minor version |
| `eks_public_access_cidrs` | *`allowed_cidrs`* | apply from CI as well as a laptop |
| `build_image` | `true` | push by hand with `infra/terraform/build-and-push.sh` |
| `app_image` | `""` | run an image from somewhere else entirely |
| `app_image_tag` | *source hash* | pin a tag instead |
| `alb_controller_chart_version` | *latest* | pin it once an apply has worked |
| `kubernetes_namespace` | `messageserver` | |
| `alb_https` | `true` | turn TLS off entirely |
| `alb_certificate_arn` | *self-signed* | a real certificate for a domain you own |
| `alb_http_redirect` | `true` | `false` closes port 80 instead of redirecting |
| `alb_ssl_policy` | TLS 1.2/1.3 | an older or stricter policy |
| `domain_name` | `""` | serve on a real name with a trusted certificate |

## Tearing it down

```sh
terraform destroy
```

The database is created with `skip_final_snapshot`, so this really does delete
it. Turn on `db_deletion_protection` if that is not what you want.

With `deploy_eks = true` you have to pass it again, or Terraform plans the
cluster as something to create rather than something to remove:

```sh
terraform destroy -var deploy_eks=true
```

That takes a while — the ALB has to go before the subnets and security groups
will release. If it stalls on the VPC or on a security group, the usual cause is
an ALB the controller has not finished deleting; delete the Ingress first
(`kubectl -n messageserver delete ingress messageserver`), wait for the load
balancer to disappear from the console, and destroy again.

## State

State is local (`infra/terraform/terraform.tfstate`, gitignored) and **contains the generated
database password in clear text** — that is how Terraform works, not something
this configuration chose. Keep it off shared machines, or move to an S3 backend
with a KMS key and DynamoDB locking if more than one person will run this.
