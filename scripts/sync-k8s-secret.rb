#!/usr/bin/env ruby
# frozen_string_literal: true

require "open3"
require "yaml"

REQUIRED_KEYS = %w[
  GITHUB_WEBHOOK_SECRET
  GITHUB_TOKEN
  OPENAI_API_KEY
].freeze

OPTIONAL_KEYS = %w[
  NOVA_SRE_API_TOKEN
  NOVA_SRE_AGENT_TOKEN
  NOVA_SRE_ALLOWED_ORIGINS
].freeze

def env_value(name)
  ENV.fetch(name, "").strip
end

def truthy?(value)
  %w[1 true yes].include?(value.to_s.strip.downcase)
end

def secret_data
  missing = REQUIRED_KEYS.select { |key| env_value(key).empty? }
  unless missing.empty?
    abort("missing required secret environment variables: #{missing.join(', ')}")
  end

  (REQUIRED_KEYS + OPTIONAL_KEYS).each_with_object({}) do |key, data|
    value = env_value(key)
    data[key] = value unless value.empty?
  end
end

def secret_manifest(namespace, name, data)
  {
    "apiVersion" => "v1",
    "kind" => "Secret",
    "metadata" => {
      "name" => name,
      "namespace" => namespace,
      "labels" => {
        "app.kubernetes.io/name" => "nova-sre",
        "app.kubernetes.io/component" => "secret"
      }
    },
    "type" => "Opaque",
    "stringData" => data
  }
end

def kubectl_apply(yaml)
  output, status = Open3.capture2e("kubectl", "apply", "-f", "-", stdin_data: yaml)
  abort(output) unless status.success?

  output
end

namespace = env_value("NOVA_SRE_SECRET_NAMESPACE")
namespace = "nova-sre" if namespace.empty?
name = env_value("NOVA_SRE_SECRET_NAME")
name = "nova-sre-secrets" if name.empty?
data = secret_data
manifest = secret_manifest(namespace, name, data)
yaml = YAML.dump(manifest)

if truthy?(ENV["NOVA_SRE_SECRET_PRINT_MANIFEST"])
  warn "warning: printing a Kubernetes Secret manifest exposes secret values"
  puts yaml
  exit
end

if truthy?(ENV["NOVA_SRE_SECRET_DRY_RUN"])
  puts "Secret #{namespace}/#{name} is ready with keys: #{data.keys.sort.join(', ')}"
  puts "Value lengths: #{data.transform_values(&:length).sort.to_h}"
  exit
end

puts kubectl_apply(yaml)
