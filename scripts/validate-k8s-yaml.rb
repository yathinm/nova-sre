#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

paths = Dir["k8s/{base,rbac,overlays}/**/*.yaml"].sort
abort("no Kubernetes YAML files found") if paths.empty?

errors = []
documents = []

paths.each do |path|
  YAML.load_stream(File.read(path)).each_with_index do |document, index|
    next if document.nil?

    documents << { path: path, index: index + 1, document: document }
    unless document.is_a?(Hash)
      errors << "#{path}:#{index + 1}: document must be a mapping"
      next
    end

    api_version = document["apiVersion"].to_s.strip
    kind = document["kind"].to_s.strip
    metadata = document["metadata"]
    name = metadata.is_a?(Hash) ? metadata["name"].to_s.strip : ""

    errors << "#{path}:#{index + 1}: missing apiVersion" if api_version.empty?
    errors << "#{path}:#{index + 1}: missing kind" if kind.empty?
    requires_metadata_name = kind != "Kustomization"
    errors << "#{path}:#{index + 1}: missing metadata.name" if requires_metadata_name && name.empty?
  rescue Psych::SyntaxError => error
    errors << "#{path}: YAML syntax error: #{error.message}"
  end
end

def pod_labels(deployment)
  deployment.dig("spec", "template", "metadata", "labels") || {}
end

def containers(deployment)
  Array(deployment.dig("spec", "template", "spec", "containers"))
end

def local_base_app?(entry)
  entry[:path].start_with?("k8s/base/") && entry[:document].is_a?(Hash) && entry[:document]["kind"] == "Deployment"
end

base_deployments = documents.select { |entry| local_base_app?(entry) }
base_services = documents.select do |entry|
  entry[:path].start_with?("k8s/base/") && entry[:document].is_a?(Hash) && entry[:document]["kind"] == "Service"
end

base_deployments.each do |entry|
  path = entry[:path]
  document = entry[:document]
  name = document.dig("metadata", "name")
  selector = document.dig("spec", "selector", "matchLabels") || {}
  labels = pod_labels(document)
  deployment_containers = containers(document)

  errors << "#{path}: Deployment/#{name} must define spec.selector.matchLabels" if selector.empty?
  unless selector.all? { |key, value| labels[key] == value }
    errors << "#{path}: Deployment/#{name} selector must match template labels"
  end
  errors << "#{path}: Deployment/#{name} must define at least one container" if deployment_containers.empty?

  deployment_containers.each do |container|
    container_name = container["name"].to_s.strip
    prefix = "#{path}: Deployment/#{name} container #{container_name.empty? ? '<unnamed>' : container_name}"

    errors << "#{prefix} must use a local image tag" unless container["image"].to_s.end_with?(":local")
    errors << "#{prefix} must set imagePullPolicy: Never for Minikube local images" unless container["imagePullPolicy"] == "Never"
    errors << "#{prefix} must define container ports" if Array(container["ports"]).empty?
    errors << "#{prefix} must define readinessProbe" unless container["readinessProbe"].is_a?(Hash)
    errors << "#{prefix} must define livenessProbe" unless container["livenessProbe"].is_a?(Hash)
    errors << "#{prefix} must define resource requests" unless container.dig("resources", "requests").is_a?(Hash)
    errors << "#{prefix} must define resource limits" unless container.dig("resources", "limits").is_a?(Hash)

    security = container["securityContext"] || {}
    errors << "#{prefix} must disable privilege escalation" unless security["allowPrivilegeEscalation"] == false
    errors << "#{prefix} must run as non-root" unless security["runAsNonRoot"] == true
    drops = Array(security.dig("capabilities", "drop"))
    errors << "#{prefix} must drop all Linux capabilities" unless drops.include?("ALL")
  end
end

base_services.each do |entry|
  path = entry[:path]
  service = entry[:document]
  name = service.dig("metadata", "name")
  namespace = service.dig("metadata", "namespace")
  selector = service.dig("spec", "selector") || {}
  ports = Array(service.dig("spec", "ports"))

  errors << "#{path}: Service/#{name} must define spec.selector" if selector.empty?
  errors << "#{path}: Service/#{name} must define ports" if ports.empty?

  matching_deployments = base_deployments.map { |deployment| deployment[:document] }.select do |deployment|
    next false if deployment.dig("metadata", "namespace") != namespace

    labels = pod_labels(deployment)
    selector.all? { |key, value| labels[key] == value }
  end

  if matching_deployments.empty?
    errors << "#{path}: Service/#{name} selector must match a base Deployment template"
    next
  end

  container_ports = matching_deployments.flat_map do |deployment|
    containers(deployment).flat_map { |container| Array(container["ports"]) }
  end
  port_numbers = container_ports.map { |port| port["containerPort"] }.compact
  port_names = container_ports.map { |port| port["name"] }.compact

  ports.each do |port|
    target = port["targetPort"] || port["port"]
    matches_target = target.is_a?(Integer) ? port_numbers.include?(target) : port_names.include?(target)
    errors << "#{path}: Service/#{name} targetPort #{target.inspect} must match a selected container port" unless matches_target
  end
end

errors << "no Kubernetes YAML documents found" if documents.empty?

if errors.any?
  warn errors.join("\n")
  exit 1
end

puts "Validated #{documents.length} Kubernetes YAML documents and local app manifest contracts"
