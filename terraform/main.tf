# Nova-SRE root Terraform configuration for the Minikube local runtime.

locals {
  observability_namespace = "observability"
  grafana_admin_password  = coalesce(var.grafana_admin_password, random_password.grafana_admin.result)
}

resource "random_password" "grafana_admin" {
  length  = 24
  special = true
}

resource "kubernetes_namespace" "observability" {
  metadata {
    name = local.observability_namespace

    labels = {
      "app.kubernetes.io/name"       = "nova-sre-observability"
      "app.kubernetes.io/managed-by" = "terraform"
      environment                    = var.environment
    }
  }
}

resource "helm_release" "prometheus" {
  name       = "prometheus"
  repository = "https://prometheus-community.github.io/helm-charts"
  chart      = "prometheus"
  namespace  = kubernetes_namespace.observability.metadata[0].name

  values = [
    file("${path.module}/prometheus-values.yaml"),
  ]
}

resource "helm_release" "grafana" {
  name       = "grafana"
  repository = "https://grafana.github.io/helm-charts"
  chart      = "grafana"
  namespace  = kubernetes_namespace.observability.metadata[0].name

  values = [
    file("${path.module}/grafana-values.yaml"),
    yamlencode({
      adminPassword = local.grafana_admin_password
    }),
  ]
}

resource "kubernetes_config_map" "grafana_pipeline_dashboard" {
  metadata {
    name      = "grafana-dashboard-nova-sre-pipeline"
    namespace = kubernetes_namespace.observability.metadata[0].name

    labels = {
      grafana_dashboard              = "1"
      "app.kubernetes.io/name"       = "nova-sre-pipeline-dashboard"
      "app.kubernetes.io/managed-by" = "terraform"
      "app.kubernetes.io/part-of"    = "nova-sre"
      "app.kubernetes.io/component"  = "observability"
    }
  }

  data = {
    "pipeline-stats.json" = file("${path.module}/../dashboards/pipeline-stats.json")
  }
}
