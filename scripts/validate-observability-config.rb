#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "net/http"
require "uri"
require "yaml"

PROMETHEUS_TARGET = "nova-sre-server.nova-sre.svc.cluster.local:8080"
PROMETHEUS_URL = "http://prometheus-server.observability.svc.cluster.local"
REQUIRED_DASHBOARD_METRICS = {
  "pipeline_jobs_total" => /pipeline_jobs_total/,
  "pipeline_active_jobs" => /pipeline_active_jobs/,
  "pipeline_scheduling_latency_seconds" => /pipeline_scheduling_latency_seconds_bucket/,
  "pipeline_agent_mttd_seconds" => /pipeline_agent_mttd_seconds_bucket/
}.freeze

def read_yaml(path)
  YAML.safe_load(File.read(path), aliases: true)
rescue Psych::SyntaxError => error
  abort("#{path}: YAML syntax error: #{error.message}")
end

def dig_array(value, *keys)
  keys.reduce(value) { |memo, key| memo.is_a?(Hash) ? memo[key] : nil }.then { |result| result.is_a?(Array) ? result : [] }
end

def expect(errors, condition, message)
  errors << message unless condition
end

def dashboard_expressions(value)
  case value
  when Hash
    value.flat_map do |key, nested|
      key == "expr" && nested.is_a?(String) ? [nested] : dashboard_expressions(nested)
    end
  when Array
    value.flat_map { |nested| dashboard_expressions(nested) }
  else
    []
  end
end

def fetch_json(base_url, path)
  uri = URI.join(base_url, path)
  response = Net::HTTP.get_response(uri)
  abort("#{uri} returned HTTP #{response.code}") unless response.code.to_i == 200

  JSON.parse(response.body)
rescue Errno::ECONNREFUSED, Errno::EHOSTUNREACH, SocketError, Net::OpenTimeout => error
  abort("#{uri || base_url} is not reachable: #{error.message}")
end

errors = []

prometheus = read_yaml("terraform/prometheus-values.yaml")
scrapes = dig_array(prometheus, "serverFiles", "prometheus.yml", "scrape_configs")
server_scrape = scrapes.find { |scrape| scrape["job_name"] == "nova-sre-server" }
expect(errors, !server_scrape.nil?, "Prometheus values must define a nova-sre-server scrape job")
if server_scrape
  targets = dig_array(server_scrape, "static_configs").flat_map { |config| Array(config["targets"]) }
  labels = dig_array(server_scrape, "static_configs").map { |config| config["labels"] }.find { |value| value.is_a?(Hash) } || {}

  expect(errors, server_scrape["metrics_path"] == "/metrics", "nova-sre-server scrape job must use /metrics")
  expect(errors, targets.include?(PROMETHEUS_TARGET), "nova-sre-server scrape job must target #{PROMETHEUS_TARGET}")
  expect(errors, labels["app"] == "nova-sre-server", "nova-sre-server scrape job must label app=nova-sre-server")
  expect(errors, labels["namespace"] == "nova-sre", "nova-sre-server scrape job must label namespace=nova-sre")
end

grafana = read_yaml("terraform/grafana-values.yaml")
datasources = dig_array(grafana, "datasources", "datasources.yaml", "datasources")
prometheus_source = datasources.find { |source| source["name"] == "Prometheus" }
expect(errors, !prometheus_source.nil?, "Grafana values must define the Prometheus datasource")
if prometheus_source
  expect(errors, prometheus_source["type"] == "prometheus", "Grafana datasource must use type=prometheus")
  expect(errors, prometheus_source["url"] == PROMETHEUS_URL, "Grafana datasource must point at #{PROMETHEUS_URL}")
  expect(errors, prometheus_source["isDefault"] == true, "Grafana datasource must be the default")
end
dashboard_sidecar = grafana.dig("sidecar", "dashboards") || {}
expect(errors, dashboard_sidecar["enabled"] == true, "Grafana dashboard sidecar must be enabled")
expect(errors, dashboard_sidecar["label"] == "grafana_dashboard", "Grafana dashboard sidecar must watch grafana_dashboard label")
expect(errors, dashboard_sidecar["labelValue"].to_s == "1", "Grafana dashboard sidecar must watch label value 1")

terraform_main = File.read("terraform/main.tf")
expect(errors, terraform_main.include?('file("${path.module}/prometheus-values.yaml")'), "Terraform must load prometheus-values.yaml")
expect(errors, terraform_main.include?('file("${path.module}/grafana-values.yaml")'), "Terraform must load grafana-values.yaml")
expect(errors, terraform_main.include?('yamlencode({'), "Terraform must inject generated Grafana admin password through yamlencode")
expect(errors, terraform_main.include?('"pipeline-stats.json" = file("${path.module}/../dashboards/pipeline-stats.json")'),
       "Terraform must provision dashboards/pipeline-stats.json through a ConfigMap")
expect(errors, terraform_main.include?("grafana_dashboard"), "Terraform dashboard ConfigMap must carry the Grafana sidecar label")

dashboard_path = "dashboards/pipeline-stats.json"
dashboard = JSON.parse(File.read(dashboard_path))
panels = dashboard["panels"]
expect(errors, dashboard["title"].to_s.include?("Nova-SRE"), "#{dashboard_path} must include a Nova-SRE dashboard title")
expect(errors, panels.is_a?(Array) && panels.any?, "#{dashboard_path} must include dashboard panels")
expressions = dashboard_expressions(dashboard)
REQUIRED_DASHBOARD_METRICS.each do |name, pattern|
  expect(errors, expressions.any? { |expr| expr.match?(pattern) }, "#{dashboard_path} must query #{name}")
end

if ENV["PROMETHEUS_BASE_URL"].to_s.strip != ""
  base_url = ENV.fetch("PROMETHEUS_BASE_URL").strip
  targets_payload = fetch_json(base_url, "/api/v1/targets")
  active_targets = targets_payload.dig("data", "activeTargets")
  matching_target = Array(active_targets).find do |target|
    target.dig("labels", "job") == "nova-sre-server" || target["scrapeUrl"].to_s.include?(PROMETHEUS_TARGET)
  end
  expect(errors, !matching_target.nil?, "Live Prometheus must expose an active nova-sre-server target")
  expect(errors, matching_target["health"] == "up", "Live Prometheus nova-sre-server target must be up") if matching_target
end

if errors.any?
  warn errors.join("\n")
  exit 1
end

puts "Validated observability config: Prometheus scrape, Grafana datasource, dashboard provisioning, dashboard metrics"
