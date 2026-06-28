#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "open3"
require "securerandom"

NAMESPACE = ENV.fetch("NOVA_SRE_NAMESPACE", "nova-sre")
CURL_IMAGE = ENV.fetch("NOVA_SRE_CURL_IMAGE", "curlimages/curl:8.11.1")
DEPLOYMENTS = %w[nova-sre-server nova-sre-agent nova-sre-frontend].freeze
SERVICES = %w[nova-sre-server nova-sre-agent nova-sre-frontend].freeze

def run(*command, stdin_data: nil)
  stdout, stderr, status = Open3.capture3(*command, stdin_data: stdin_data)
  unless status.success?
    abort("#{command.join(' ')} failed\n#{stderr.strip}\n#{stdout.strip}".strip)
  end
  stdout
end

def kubectl_json(*args)
  JSON.parse(run("kubectl", *args, "-o", "json"))
end

def deployment_ready?(deployment)
  desired = deployment.dig("spec", "replicas").to_i
  desired = 1 if desired <= 0
  ready = deployment.dig("status", "readyReplicas").to_i
  updated = deployment.dig("status", "updatedReplicas").to_i
  available = deployment.dig("status", "availableReplicas").to_i
  ready >= desired && updated >= desired && available >= desired
end

def deployment_env_names(deployment)
  containers = deployment.dig("spec", "template", "spec", "containers")
  Array(containers).flat_map { |container| Array(container["env"]).map { |env| env["name"] } }.compact
end

DEPLOYMENTS.each do |name|
  deployment = kubectl_json("-n", NAMESPACE, "get", "deployment", name)
  abort("deployment/#{name} is not ready") unless deployment_ready?(deployment)

  puts "deployment ok: #{name}"
end

SERVICES.each do |name|
  service = kubectl_json("-n", NAMESPACE, "get", "service", name)
  ports = Array(service.dig("spec", "ports")).map { |port| port["port"] }
  abort("service/#{name} has no ports") if ports.empty?

  puts "service ok: #{name} ports=#{ports.join(',')}"
end

server_env = deployment_env_names(kubectl_json("-n", NAMESPACE, "get", "deployment", "nova-sre-server"))
agent_env = deployment_env_names(kubectl_json("-n", NAMESPACE, "get", "deployment", "nova-sre-agent"))
agent_token_configured = server_env.include?("NOVA_SRE_AGENT_TOKEN") && agent_env.include?("NOVA_SRE_AGENT_TOKEN")
abort("server and agent deployments must both reference NOVA_SRE_AGENT_TOKEN") unless agent_token_configured
puts "agent token env ok"

health_pod = "nova-sre-cluster-health-#{SecureRandom.hex(4)}"
health = run(
  "kubectl", "run", health_pod,
  "--rm", "-i", "--restart=Never",
  "-n", NAMESPACE,
  "--image=#{CURL_IMAGE}",
  "--",
  "-fsS", "http://nova-sre-agent:8000/healthz"
)
abort("agent health through service returned unexpected body") unless health.include?('"status":"ok"')
puts "agent service health ok"

unauth_pod = "nova-sre-agent-auth-#{SecureRandom.hex(4)}"
status_output = run(
  "kubectl", "run", unauth_pod,
  "--rm", "-i", "--restart=Never",
  "-n", NAMESPACE,
  "--image=#{CURL_IMAGE}",
  "--",
  "-s", "-o", "/dev/null", "-w", "%{http_code}",
  "-X", "POST", "http://nova-sre-agent:8000/diagnose",
  "-H", "Content-Type: application/json",
  "-d", "{}"
)
status = status_output[/\d{3}/]
abort("agent /diagnose should reject unauthenticated requests, got HTTP #{status_output.strip}") unless status == "401"
puts "agent unauthenticated diagnose rejected"

puts "Cluster runtime validation passed"
