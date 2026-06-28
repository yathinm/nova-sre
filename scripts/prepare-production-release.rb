#!/usr/bin/env ruby
# frozen_string_literal: true

require "English"
require "open3"

DEFAULT_KUSTOMIZATION = "k8s/overlays/production/kustomization.yaml"

def env_value(name)
  ENV.fetch(name, "").strip
end

def run!(env, *command)
  puts "+ #{command.join(' ')}"
  output, status = Open3.capture2e(env, *command)
  puts output unless output.empty?
  abort("#{command.join(' ')} failed") unless status.success?
end

release_tag = env_value("RELEASE_TAG")
abort("RELEASE_TAG is required") if release_tag.empty?

kustomization_path = env_value("KUSTOMIZATION_PATH")
kustomization_path = DEFAULT_KUSTOMIZATION if kustomization_path.empty?
env = {
  "RELEASE_TAG" => release_tag,
  "PRODUCTION_IMAGE_REGISTRY" => env_value("PRODUCTION_IMAGE_REGISTRY"),
  "KUSTOMIZATION_PATH" => kustomization_path
}.reject { |_, value| value.empty? }

run!(env, "ruby", "scripts/set-production-images.rb")
run!({}, "ruby", "scripts/validate-release-tools.rb")
run!({}, "ruby", "scripts/validate-secrets.rb")

if kustomization_path == DEFAULT_KUSTOMIZATION
  run!({}, "ruby", "scripts/validate-production-k8s.rb")
else
  puts "Skipped production overlay validation for custom KUSTOMIZATION_PATH=#{kustomization_path}"
end

puts "Production release preflight passed for #{release_tag}"
