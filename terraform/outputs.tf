output "minikube_profile" {
  description = "Minikube profile used for local Nova-SRE infrastructure"
  value       = var.minikube_profile
}

output "kube_context" {
  description = "Kubernetes context targeted by Terraform"
  value       = var.minikube_profile
}

output "observability_namespace" {
  description = "Kubernetes namespace for local observability services"
  value       = kubernetes_namespace.observability.metadata[0].name
}

output "prometheus_release_name" {
  description = "Helm release name for Prometheus"
  value       = helm_release.prometheus.name
}

output "prometheus_server_service_name" {
  description = "Prometheus server service name for kubectl port-forward"
  value       = "${helm_release.prometheus.name}-server"
}

output "prometheus_port_forward_command" {
  description = "Local command to forward Prometheus to http://localhost:9090"
  value       = "kubectl --context ${var.minikube_profile} -n ${kubernetes_namespace.observability.metadata[0].name} port-forward svc/${helm_release.prometheus.name}-server 9090:80"
}

output "grafana_release_name" {
  description = "Helm release name for Grafana"
  value       = helm_release.grafana.name
}

output "grafana_service_name" {
  description = "Grafana service name for kubectl port-forward"
  value       = helm_release.grafana.name
}

output "grafana_port_forward_command" {
  description = "Local command to forward Grafana to http://localhost:3000"
  value       = "kubectl --context ${var.minikube_profile} -n ${kubernetes_namespace.observability.metadata[0].name} port-forward svc/${helm_release.grafana.name} 3000:80"
}
