#!/usr/bin/env ruby
# frozen_string_literal: true

require "fileutils"
require "open3"
require "tmpdir"
require "yaml"

SOURCE = "k8s/overlays/production/kustomization.yaml"

Dir.mktmpdir("nova-sre-release-tools-") do |dir|
  target = File.join(dir, "kustomization.yaml")
  FileUtils.cp(SOURCE, target)

  env = {
    "KUSTOMIZATION_PATH" => target,
    "PRODUCTION_IMAGE_REGISTRY" => "registry.test/nova-sre",
    "RELEASE_TAG" => "v9.8.7"
  }
  output, status = Open3.capture2e(env, "ruby", "scripts/set-production-images.rb")
  abort(output) unless status.success?

  document = YAML.safe_load(File.read(target), aliases: true)
  images = document.fetch("images")
  expected = {
    "nova-sre-server" => "registry.test/nova-sre/server:v9.8.7",
    "nova-sre-agent" => "registry.test/nova-sre/agent:v9.8.7",
    "nova-sre-frontend" => "registry.test/nova-sre/frontend:v9.8.7"
  }
  actual = images.to_h do |image|
    [image.fetch("name"), "#{image.fetch('newName')}:#{image.fetch('newTag')}"]
  end
  unless expected.all? { |name, value| actual[name] == value }
    abort("unexpected image replacements: #{actual.inspect}")
  end
end

puts "Validated release tooling: production image stamping"
