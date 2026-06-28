variable "minikube_profile" {
  description = "Minikube profile and Kubernetes context used for local Nova-SRE infrastructure"
  type        = string
  default     = "nova-sre"
}

variable "environment" {
  description = "Local deployment environment label"
  type        = string
  default     = "local"
}

variable "grafana_admin_password" {
  description = "Optional Grafana admin password. When unset, Terraform generates a local random password."
  type        = string
  default     = null
  sensitive   = true
}
