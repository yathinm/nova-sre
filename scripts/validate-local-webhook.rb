#!/usr/bin/env ruby
# frozen_string_literal: true

require "base64"
require "English"
require "json"
require "net/http"
require "openssl"
require "securerandom"
require "uri"

api_base = ENV.fetch("NOVA_SRE_API_BASE", "http://localhost:8080").strip
api_token = ENV.fetch("NOVA_SRE_API_TOKEN", "").strip
namespace = ENV.fetch("NOVA_SRE_NAMESPACE", "nova-sre").strip
secret_name = ENV.fetch("NOVA_SRE_SECRET_NAME", "nova-sre-secrets").strip

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
  Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https", open_timeout: 3, read_timeout: 10) do |http|
    http.request(request)
  end
rescue Errno::ECONNREFUSED, Errno::EHOSTUNREACH, SocketError, Net::OpenTimeout => error
  abort("#{uri} is not reachable: #{error.message}")
end

def expect_status(response, statuses, description)
  return if statuses.include?(response.code.to_i)

  abort("#{description} returned HTTP #{response.code}; expected #{statuses.join(' or ')}")
end

def secret_from_kubernetes(namespace, secret_name)
  output = IO.popen(
    [
      "kubectl", "-n", namespace, "get", "secret", secret_name,
      "-o", "jsonpath={.data.GITHUB_WEBHOOK_SECRET}"
    ],
    &:read
  )
  return "" unless $CHILD_STATUS&.success?

  Base64.decode64(output).strip
rescue Errno::ENOENT
  ""
end

def webhook_secret(namespace, secret_name)
  env_secret = ENV.fetch("GITHUB_WEBHOOK_SECRET", "").strip
  return env_secret unless env_secret.empty?

  secret_from_kubernetes(namespace, secret_name)
end

def api_get_json(uri, api_token)
  get = Net::HTTP::Get.new(uri)
  get["Authorization"] = "Bearer #{api_token}" unless api_token.empty?
  response = request(uri, get)
  expect_status(response, [200], "GET #{uri}")
  JSON.parse(response.body)
end

def signed_ping_request(webhook_uri, secret, delivery_id, payload, user_agent)
  signature = "sha256=#{OpenSSL::HMAC.hexdigest('SHA256', secret, payload)}"
  post = Net::HTTP::Post.new(webhook_uri)
  post["Content-Type"] = "application/json"
  post["User-Agent"] = user_agent
  post["X-GitHub-Delivery"] = delivery_id
  post["X-GitHub-Event"] = "ping"
  post["X-Hub-Signature-256"] = signature
  post.body = payload
  post
end

def wait_for_event(events_uri, api_token, delivery_id, expected_statuses)
  found = nil
  10.times do
    payload = api_get_json(events_uri, api_token)
    found = Array(payload["events"]).find { |event| event["delivery_id"] == delivery_id }
    break if found && (expected_statuses.empty? || expected_statuses.include?(found["status"]))

    sleep 0.5
  end
  found
end

secret = webhook_secret(namespace, secret_name)
abort("set GITHUB_WEBHOOK_SECRET or make #{secret_name} readable in namespace #{namespace}") if secret.empty?

health_uri = uri_for(api_base, "/healthz")
health = request(health_uri, Net::HTTP::Get.new(health_uri))
expect_status(health, [200], "GET #{health_uri}")
abort("GET #{health_uri} returned unexpected body") unless health.body.to_s.strip == "ok"
puts "api health ok: #{health_uri}"

webhook_uri = uri_for(api_base, "/webhook")
invalid_payload = JSON.generate({ zen: "Reject invalid webhook signatures." })
invalid_delivery_id = "local-webhook-invalid-#{SecureRandom.uuid}"
invalid = Net::HTTP::Post.new(webhook_uri)
invalid["Content-Type"] = "application/json"
invalid["User-Agent"] = "nova-sre-local-webhook-validator"
invalid["X-GitHub-Delivery"] = invalid_delivery_id
invalid["X-GitHub-Event"] = "ping"
invalid["X-Hub-Signature-256"] = "sha256=#{'0' * 64}"
invalid.body = invalid_payload
invalid_response = request(webhook_uri, invalid)
expect_status(invalid_response, [401], "POST #{webhook_uri} with invalid signature")
puts "invalid signature rejected: delivery=#{invalid_delivery_id}"

payload = JSON.generate(
  {
    zen: "Validate the local Nova-SRE webhook path.",
    hook_id: 0,
    repository: {
      full_name: "local/webhook-validation"
    }
  }
)
delivery_id = "local-webhook-#{SecureRandom.uuid}"

post = signed_ping_request(webhook_uri, secret, delivery_id, payload, "nova-sre-local-webhook-validator")
delivery = request(webhook_uri, post)
expect_status(delivery, [202], "POST #{webhook_uri}")
puts "signed ping accepted: delivery=#{delivery_id}"

events_uri = uri_for(api_base, "/api/events")
events_uri.query = "limit=20"
found = wait_for_event(events_uri, api_token, delivery_id, [])

abort("delivery #{delivery_id} was not visible in /api/events") unless found
puts "delivery visible: status=#{found['status']} event=#{found['event']}"

duplicate_delivery_id = "local-webhook-duplicate-#{SecureRandom.uuid}"
duplicate_payload = JSON.generate(
  {
    zen: "Validate Nova-SRE duplicate delivery handling.",
    hook_id: 0,
    repository: {
      full_name: "local/webhook-validation"
    }
  }
)
duplicate_post = signed_ping_request(webhook_uri, secret, duplicate_delivery_id, duplicate_payload, "nova-sre-local-webhook-validator")
expect_status(request(webhook_uri, duplicate_post), [202], "first duplicate probe POST #{webhook_uri}")
duplicate_retry = signed_ping_request(webhook_uri, secret, duplicate_delivery_id, duplicate_payload, "nova-sre-local-webhook-validator")
expect_status(request(webhook_uri, duplicate_retry), [202], "second duplicate probe POST #{webhook_uri}")
duplicate = wait_for_event(events_uri, api_token, duplicate_delivery_id, ["duplicate"])
abort("duplicate delivery #{duplicate_delivery_id} was not visible as duplicate in /api/events") unless duplicate
puts "duplicate delivery visible: delivery=#{duplicate_delivery_id}"

summary_uri = uri_for(api_base, "/api/summary")
summary = api_get_json(summary_uri, api_token)
ping_count = summary.fetch("by_event", {}).fetch("ping", 0).to_i
abort("/api/summary did not include ping activity") if ping_count <= 0
puts "summary visible: ping=#{ping_count} total=#{summary['total']}"
puts "Local signed webhook validation passed"
