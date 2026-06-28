#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "net/http"
require "openssl"
require "securerandom"
require "uri"

def env_value(*names)
  names.each do |name|
    value = ENV.fetch(name, "").strip
    return value unless value.empty?
  end
  ""
end

def normalize_urls(raw_url)
  abort("set WEBHOOK_BASE_URL or WEBHOOK_URL") if raw_url.empty?

  uri = URI.parse(raw_url)
  abort("webhook URL must use http or https") unless %w[http https].include?(uri.scheme)
  abort("webhook URL must include a host") if uri.host.nil? || uri.host.empty?

  path = uri.path.to_s
  if path.end_with?("/webhook")
    uri.path = path.delete_suffix("/webhook")
  end
  uri.path = "/" if uri.path.nil? || uri.path.empty?
  uri.query = nil
  uri.fragment = nil

  health = uri.dup
  health.path = join_path(uri.path, "healthz")

  webhook = uri.dup
  webhook.path = join_path(uri.path, "webhook")

  [health, webhook]
rescue URI::InvalidURIError => error
  abort("invalid webhook URL: #{error.message}")
end

def join_path(base, suffix)
  cleaned = base.to_s.sub(%r{/+\z}, "")
  cleaned = "" if cleaned == "/"
  "#{cleaned}/#{suffix}"
end

def request(uri, request)
  Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https", open_timeout: 5, read_timeout: 10) do |http|
    http.request(request)
  end
end

def expect_response(response, allowed_codes, description)
  return if allowed_codes.include?(response.code.to_i)

  abort("#{description} returned HTTP #{response.code}; expected #{allowed_codes.join(' or ')}")
end

raw_url = env_value("WEBHOOK_BASE_URL", "WEBHOOK_URL")
secret = env_value("GITHUB_WEBHOOK_SECRET")
health_url, webhook_url = normalize_urls(raw_url)

health = request(health_url, Net::HTTP::Get.new(health_url))
expect_response(health, [200], "GET #{health_url}")
abort("GET #{health_url} returned an unexpected body") unless health.body.to_s.strip == "ok"
puts "health ok: #{health_url}"

probe = request(webhook_url, Net::HTTP::Get.new(webhook_url))
expect_response(probe, [405], "GET #{webhook_url}")
abort("GET #{webhook_url} did not advertise Allow: POST") unless probe["Allow"].to_s.split(/\s*,\s*/).include?("POST")
puts "webhook route ok: #{webhook_url}"

if secret.empty?
  puts "signed ping skipped: set GITHUB_WEBHOOK_SECRET to verify webhook signature and enqueue path"
  exit 0
end

payload = JSON.generate(
  {
    zen: "Validate the Nova-SRE webhook path.",
    hook_id: 0,
    repository: {
      full_name: "local/webhook-validation"
    }
  }
)
signature = "sha256=#{OpenSSL::HMAC.hexdigest('SHA256', secret, payload)}"
delivery_id = "local-validation-#{SecureRandom.uuid}"

post = Net::HTTP::Post.new(webhook_url)
post["Content-Type"] = "application/json"
post["User-Agent"] = "nova-sre-webhook-validator"
post["X-GitHub-Delivery"] = delivery_id
post["X-GitHub-Event"] = "ping"
post["X-Hub-Signature-256"] = signature
post.body = payload

delivery = request(webhook_url, post)
expect_response(delivery, [202], "POST #{webhook_url}")
puts "signed ping accepted: delivery=#{delivery_id}"
