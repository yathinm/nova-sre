#!/usr/bin/env ruby
# frozen_string_literal: true

require "open3"
require "yaml"

def expect(errors, condition, message)
  errors << message unless condition
end

output, status = Open3.capture2e("kubectl", "kustomize", "k8s/overlays/production")
abort(output) unless status.success?

documents = YAML.load_stream(output).compact
errors = []

ingress = documents.find { |doc| doc["kind"] == "Ingress" && doc.dig("metadata", "name") == "nova-sre" }
expect(errors, !ingress.nil?, "production overlay must render the nova-sre Ingress")
if ingress
  tls = Array(ingress.dig("spec", "tls"))
  rules = Array(ingress.dig("spec", "rules"))
  paths = rules.flat_map { |rule| Array(rule.dig("http", "paths")) }

  expect(errors, ingress.dig("spec", "ingressClassName").to_s != "", "Ingress must set ingressClassName")
  expect(errors, tls.any? { |entry| entry["secretName"].to_s != "" && Array(entry["hosts"]).any? },
         "Ingress must define TLS hosts and a secretName")
  expect(errors, paths.any? { |path| path["path"] == "/" && path.dig("backend", "service", "name") == "nova-sre-frontend" },
         "Ingress must route / to the frontend service")
  expect(errors, paths.any? { |path| path["path"] == "/api" && path.dig("backend", "service", "name") == "nova-sre-server" },
         "Ingress must route /api to the server service")
  expect(errors, paths.any? { |path| path["path"] == "/webhook" && path.dig("backend", "service", "name") == "nova-sre-server" },
         "Ingress must route /webhook to the server service")
end

server = documents.find { |doc| doc["kind"] == "Deployment" && doc.dig("metadata", "name") == "nova-sre-server" }
expect(errors, !server.nil?, "production overlay must render the server Deployment")
if server
  containers = Array(server.dig("spec", "template", "spec", "containers"))
  server_container = containers.find { |container| container["name"] == "server" }
  env = Array(server_container && server_container["env"])
  env_by_name = env.to_h { |entry| [entry["name"], entry] }
  volume = Array(server.dig("spec", "template", "spec", "volumes")).find { |entry| entry["name"] == "activity-store" }

  expect(errors, env_by_name.dig("NOVA_SRE_API_TOKEN", "valueFrom", "secretKeyRef", "name") == "nova-sre-secrets",
         "server must read NOVA_SRE_API_TOKEN from nova-sre-secrets")
  expect(errors, env_by_name.dig("NOVA_SRE_ALLOWED_ORIGINS", "valueFrom", "secretKeyRef", "name") == "nova-sre-secrets",
         "server must read NOVA_SRE_ALLOWED_ORIGINS from nova-sre-secrets")
  expect(errors, env_by_name["NOVA_SRE_ACTIVITY_STORE_PATH"].to_s != "",
         "server must set NOVA_SRE_ACTIVITY_STORE_PATH")
  expect(errors, volume && volume.dig("persistentVolumeClaim", "claimName") == "nova-sre-activity-store",
         "production server activity-store volume must use the PVC")
end

pvc = documents.find { |doc| doc["kind"] == "PersistentVolumeClaim" && doc.dig("metadata", "name") == "nova-sre-activity-store" }
expect(errors, !pvc.nil?, "production overlay must render the activity store PVC")

if errors.any?
  warn errors.join("\n")
  exit 1
end

puts "Validated production Kubernetes overlay: TLS ingress, API auth/CORS wiring, and activity PVC"
