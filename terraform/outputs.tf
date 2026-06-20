output "minikube_profile" {
  description = "Minikube profile used for local Nova-SRE infrastructure"
  value       = var.minikube_profile
}

output "kube_context" {
  description = "Kubernetes context targeted by Terraform"
  value       = var.minikube_profile
}
