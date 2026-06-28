#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

DEFAULT_PATH = "k8s/overlays/production/kustomization.yaml"
DEFAULT_REGISTRY = "registry.example.com/nova-sre"
COMPONENTS = {
  "nova-sre-server" => "server",
  "nova-sre-agent" => "agent",
  "nova-sre-frontend" => "frontend"
}.freeze

def env_value(name, fallback = nil)
  value = ENV.fetch(name, "").strip
  value.empty? ? fallback : value
end

def abort_with_usage(message)
  warn message
  warn "usage: RELEASE_TAG=v1.2.3 [PRODUCTION_IMAGE_REGISTRY=registry.example.com/nova-sre] make set-production-images"
  exit 1
end

release_tag = env_value("RELEASE_TAG")
registry = env_value("PRODUCTION_IMAGE_REGISTRY", DEFAULT_REGISTRY)
path = env_value("KUSTOMIZATION_PATH", DEFAULT_PATH)

abort_with_usage("RELEASE_TAG is required") if release_tag.nil?
abort_with_usage("RELEASE_TAG must not contain whitespace") if release_tag.match?(/\s/)
abort_with_usage("PRODUCTION_IMAGE_REGISTRY must not contain whitespace") if registry.match?(/\s/)

document = YAML.safe_load(File.read(path), aliases: true)
abort("#{path}: expected a Kustomization mapping") unless document.is_a?(Hash)

images = document["images"]
abort("#{path}: expected images to be a list") unless images.is_a?(Array)

by_name = images.each_with_object({}) do |image, memo|
  next unless image.is_a?(Hash)

  memo[image["name"]] = image
end
missing = COMPONENTS.keys.reject { |name| by_name.key?(name) }
abort("#{path}: missing image entries for #{missing.join(', ')}") unless missing.empty?

COMPONENTS.each do |name, component|
  image = by_name.fetch(name)
  image["newName"] = "#{registry}/#{component}"
  image["newTag"] = release_tag
end

File.write(path, YAML.dump(document))
puts "Updated #{path} images to #{registry}:#{release_tag}"
