#!/usr/bin/env ruby
# frozen_string_literal: true

require "open3"

sample_env = {
  "GITHUB_WEBHOOK_SECRET" => "replace-with-local-webhook-secret",
  "GITHUB_TOKEN" => "replace-with-local-github-token",
  "OPENAI_API_KEY" => "replace-with-local-openai-api-key",
  "NOVA_SRE_API_TOKEN" => "replace-with-optional-control-panel-token",
  "NOVA_SRE_AGENT_TOKEN" => "replace-with-optional-server-to-agent-token",
  "NOVA_SRE_ALLOWED_ORIGINS" => "http://localhost:8081",
  "NOVA_SRE_SECRET_DRY_RUN" => "true"
}

output, status = Open3.capture2e(sample_env, "ruby", "scripts/sync-k8s-secret.rb")
abort(output) unless status.success?

required_fragments = [
  "Secret nova-sre/nova-sre-secrets is ready",
  "GITHUB_WEBHOOK_SECRET",
  "GITHUB_TOKEN",
  "OPENAI_API_KEY",
  "NOVA_SRE_API_TOKEN",
  "NOVA_SRE_AGENT_TOKEN",
  "NOVA_SRE_ALLOWED_ORIGINS",
  "Value lengths"
]
missing = required_fragments.reject { |fragment| output.include?(fragment) }
abort("secret sync validation missed expected output: #{missing.join(', ')}") unless missing.empty?

missing_output, missing_status = Open3.capture2e(
  {
    "GITHUB_WEBHOOK_SECRET" => "",
    "GITHUB_TOKEN" => "replace-with-local-github-token",
    "OPENAI_API_KEY" => "replace-with-local-openai-api-key",
    "NOVA_SRE_SECRET_DRY_RUN" => "true"
  },
  "ruby",
  "scripts/sync-k8s-secret.rb"
)
if missing_status.success? || !missing_output.include?("missing required secret environment variables: GITHUB_WEBHOOK_SECRET")
  abort("secret sync validation expected missing required key failure, got: #{missing_output}")
end

puts "Validated Kubernetes secret sync helper"
