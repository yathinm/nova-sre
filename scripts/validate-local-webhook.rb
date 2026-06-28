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

secret = webhook_secret(namespace, secret_name)
abort("set GITHUB_WEBHOOK_SECRET or make #{secret_name} readable in namespace #{namespace}") if secret.empty?

health_uri = uri_for(api_base, "/healthz")
health = request(health_uri, Net::HTTP::Get.new(health_uri))
expect_status(health, [200], "GET #{health_uri}")
abort("GET #{health_uri} returned unexpected body") unless health.body.to_s.strip == "ok"
puts "api health ok: #{health_uri}"

webhook_uri = uri_for(api_base, "/webhook")
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
signature = "sha256=#{OpenSSL::HMAC.hexdigest('SHA256', secret, payload)}"

post = Net::HTTP::Post.new(webhook_uri)
post["Content-Type"] = "application/json"
post["User-Agent"] = "nova-sre-local-webhook-validator"
post["X-GitHub-Delivery"] = delivery_id
post["X-GitHub-Event"] = "ping"
post["X-Hub-Signature-256"] = signature
post.body = payload

delivery = request(webhook_uri, post)
expect_status(delivery, [202], "POST #{webhook_uri}")
puts "signed ping accepted: delivery=#{delivery_id}"

events_uri = uri_for(api_base, "/api/events")
events_uri.query = "limit=20"
found = nil
10.times do
  payload = api_get_json(events_uri, api_token)
  found = Array(payload["events"]).find { |event| event["delivery_id"] == delivery_id }
  break if found

  sleep 0.5
end

abort("delivery #{delivery_id} was not visible in /api/events") unless found
puts "delivery visible: status=#{found['status']} event=#{found['event']}"
puts "Local signed webhook validation passed"
