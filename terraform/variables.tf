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
