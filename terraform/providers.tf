terraform {
  required_version = ">= 1.6.0"

  required_providers {
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.0"
    }

    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = var.minikube_profile
}

provider "helm" {
  kubernetes = {
    config_path    = "~/.kube/config"
    config_context = var.minikube_profile
  }
}
