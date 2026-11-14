# Scaleway Serverless Container deployment for the #385 development-secrets
# gateway. All credentials are supplied as OpenTofu variables at apply time.
# This file creates no Secret Manager secrets: the gateway owns its two
# protected per-project secrets at runtime.
terraform {
  required_version = ">= 1.9.0"

  required_providers {
    scaleway = {
      source  = "scaleway/scaleway"
      version = "~> 2.74"
    }
  }
}

provider "scaleway" {
  region      = var.region
  project_id  = var.project_id
  access_key  = var.scw_access_key
  secret_key  = var.scw_secret_key
}

variable "region" {
  type        = string
  description = "Scaleway region for the dedicated codebox-dev-secrets project."
  default     = "fr-par"
}

variable "project_id" {
  type        = string
  description = "Scaleway project ID of the dedicated codebox-dev-secrets project."
}

variable "scw_access_key" {
  type        = string
  description = "Scaleway IAM application access key used to deploy the container."
  sensitive   = true
}

variable "scw_secret_key" {
  type        = string
  description = "Scaleway IAM application secret key used to deploy the container."
  sensitive   = true
}

variable "gateway_public_url" {
  type        = string
  description = "Public HTTPS origin of the gateway, used for the ProConnect redirect URI."
}

variable "gateway_token_seed" {
  type        = string
  description = "Base64url Ed25519 seed used to sign and verify gateway session tokens."
  sensitive   = true
}

variable "proconnect_issuer" {
  type        = string
  description = "ProConnect OIDC issuer, including /api/v2."
  default     = "https://fca.integ01.dev-agentconnect.fr/api/v2"
}

variable "proconnect_client_id" {
  type        = string
  description = "ProConnect OIDC client ID."
  sensitive   = true
}

variable "proconnect_client_secret" {
  type        = string
  description = "ProConnect OIDC client secret."
  sensitive   = true
}

variable "registry_namespace_name" {
  type        = string
  description = "Existing Scaleway Container Registry namespace holding the gateway image."
}

variable "registry_image_name" {
  type        = string
  description = "Existing Scaleway Container Registry image name for the gateway."
  default     = "secrets-gateway"
}

variable "image_tag" {
  type        = string
  description = "Immutable image tag to deploy, normally secrets-gateway-vX.Y.Z."
}

variable "allowed_sirets" {
  type        = string
  description = "Optional comma-separated SIRET allowlist for project creation."
  default     = ""
}

data "scaleway_registry_namespace" "gateway" {
  name = var.registry_namespace_name
}

data "scaleway_registry_image" "gateway" {
  namespace_id = data.scaleway_registry_namespace.gateway.id
  name         = var.registry_image_name
}

data "scaleway_registry_image_tag" "gateway" {
  image_id = data.scaleway_registry_image.gateway.id
  name     = var.image_tag
}

resource "scaleway_container_namespace" "gateway" {
  name        = "codebox-secrets-gateway"
  description = "Stateless development-secrets gateway for codebox"
  project_id  = var.project_id
}

resource "scaleway_container" "gateway" {
  name         = "codebox-secrets-gateway"
  namespace_id = scaleway_container_namespace.gateway.id
  image        = "${data.scaleway_registry_namespace.gateway.endpoint}/${data.scaleway_registry_image.gateway.name}:${data.scaleway_registry_image_tag.gateway.name}"
  # The immutable tag digest forces a redeploy when the released image changes.
  registry_sha256 = data.scaleway_registry_image_tag.gateway.digest

  port                    = 8080
  cpu_limit               = 512
  memory_limit_bytes      = 268435456
  # Pending ProConnect login attempts and single-use terminal tickets are held
  # in process-local memory (see AttemptStore). Keep exactly one warm replica
  # until those are moved to shared storage: a callback hitting another
  # replica would be rejected, and scaling to zero can discard a login in
  # progress.
  min_scale               = 1
  max_scale               = 1
  privacy                 = "public"
  protocol                = "http1"
  https_connections_only  = true

  environment_variables = {
    GATEWAY_PUBLIC_URL      = var.gateway_public_url
    PROCONNECT_ISSUER       = var.proconnect_issuer
    SCW_PROJECT_ID          = var.project_id
    SCW_REGION              = var.region
    GATEWAY_ALLOWED_SIRETS  = var.allowed_sirets
  }

  secret_environment_variables = {
    GATEWAY_TOKEN_SEED       = var.gateway_token_seed
    PROCONNECT_CLIENT_ID     = var.proconnect_client_id
    PROCONNECT_CLIENT_SECRET = var.proconnect_client_secret
    SCW_SECRET_KEY           = var.scw_secret_key
  }
}

output "container_domain_name" {
  value       = scaleway_container.gateway.domain_name
  description = "Native HTTPS domain of the deployed gateway."
}

output "container_status" {
  value = scaleway_container.gateway.status
}
