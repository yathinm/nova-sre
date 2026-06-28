#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "net/http"
require "uri"

api_base = ENV.fetch("NOVA_SRE_API_BASE", "http://localhost:8080").strip
frontend_base = ENV.fetch("NOVA_SRE_FRONTEND_BASE", "http://localhost:8081").strip
origin = ENV.fetch("NOVA_SRE_FRONTEND_ORIGIN", frontend_base).strip

def uri_for(base, path)
  uri = URI.parse(base)
  abort("invalid URL #{base.inspect}") unless %w[http https].include?(uri.scheme) && uri.host

  base_path = uri.path.to_s.sub(%r{/+\z}, "")
  uri.path = "#{base_path}#{path}"
  uri.query = nil
  uri.fragment = nil
  uri
rescue URI::InvalidURIError => error
  abort("invalid URL #{base.inspect}: #{error.message}")
end

def request(uri, request)
  Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https", open_timeout: 3, read_timeout: 8) do |http|
    http.request(request)
  end
rescue Errno::ECONNREFUSED, Errno::EHOSTUNREACH, SocketError, Net::OpenTimeout => error
  abort("#{uri} is not reachable: #{error.message}")
end

def expect_status(response, status, description)
  return if response.code.to_i == status

  abort("#{description} returned HTTP #{response.code}; expected #{status}")
end

health_uri = uri_for(api_base, "/healthz")
health = request(health_uri, Net::HTTP::Get.new(health_uri))
expect_status(health, 200, "GET #{health_uri}")
abort("GET #{health_uri} returned unexpected body #{health.body.inspect}") unless health.body.to_s.strip == "ok"
puts "api health ok: #{health_uri}"

metrics_uri = uri_for(api_base, "/metrics")
metrics = request(metrics_uri, Net::HTTP::Get.new(metrics_uri))
expect_status(metrics, 200, "GET #{metrics_uri}")
unless metrics.body.to_s.match?(/(^# HELP go_|pipeline_jobs_total)/)
  abort("GET #{metrics_uri} did not look like Prometheus metrics")
end
puts "api metrics ok: #{metrics_uri}"

config_uri = uri_for(api_base, "/api/config")
config = request(config_uri, Net::HTTP::Get.new(config_uri))
expect_status(config, 200, "GET #{config_uri}")
config_payload = JSON.parse(config.body)
unless config_payload.key?("activity_limit") && config_payload.key?("delivery_cache_ttl") && config_payload.key?("agent_auth_enabled")
  abort("GET #{config_uri} did not include expected runtime config keys")
end
sensitive_keys = config_payload.keys.grep(/secret|token|password|key/i)
abort("GET #{config_uri} exposed sensitive-looking keys: #{sensitive_keys.join(', ')}") if sensitive_keys.any?
puts "api config ok: #{config_uri}"

cors_request = Net::HTTP::Get.new(health_uri)
cors_request["Origin"] = origin
cors = request(health_uri, cors_request)
allowed_origin = cors["Access-Control-Allow-Origin"].to_s
if allowed_origin.empty?
  abort("GET #{health_uri} did not include Access-Control-Allow-Origin for #{origin}")
end
puts "api cors ok: #{allowed_origin}"

frontend_uri = uri_for(frontend_base, "/")
frontend = request(frontend_uri, Net::HTTP::Get.new(frontend_uri))
expect_status(frontend, 200, "GET #{frontend_uri}")
unless frontend.body.to_s.include?("Nova-SRE Control Panel")
  abort("GET #{frontend_uri} did not return the Nova-SRE control panel")
end
puts "frontend ok: #{frontend_uri}"

summary_uri = uri_for(api_base, "/api/summary")
summary = request(summary_uri, Net::HTTP::Get.new(summary_uri))
expect_status(summary, 200, "GET #{summary_uri}")
summary_payload = JSON.parse(summary.body)
abort("GET #{summary_uri} did not include total") unless summary_payload.key?("total")
puts "api summary ok: #{summary_uri}"
