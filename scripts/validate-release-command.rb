#!/usr/bin/env ruby
# frozen_string_literal: true

require "fileutils"
require "open3"
require "tmpdir"
require "yaml"

source = "k8s/overlays/production/kustomization.yaml"

Dir.mktmpdir("nova-sre-release-command-") do |dir|
  target = File.join(dir, "kustomization.yaml")
  FileUtils.cp(source, target)

  env = {
    "KUSTOMIZATION_PATH" => target,
    "PRODUCTION_IMAGE_REGISTRY" => "registry.test/nova-sre",
    "RELEASE_TAG" => "v1.2.3"
  }
  output, status = Open3.capture2e(env, "ruby", "scripts/prepare-production-release.rb")
  abort(output) unless status.success?
  abort("release command did not report success: #{output}") unless output.include?("Production release preflight passed for v1.2.3")

  document = YAML.safe_load(File.read(target), aliases: true)
  images = document.fetch("images")
  actual = images.to_h { |image| [image.fetch("name"), "#{image.fetch('newName')}:#{image.fetch('newTag')}"] }
  expected = {
    "nova-sre-server" => "registry.test/nova-sre/server:v1.2.3",
    "nova-sre-agent" => "registry.test/nova-sre/agent:v1.2.3",
    "nova-sre-frontend" => "registry.test/nova-sre/frontend:v1.2.3"
  }
  unless expected.all? { |name, value| actual[name] == value }
    abort("unexpected release image replacements: #{actual.inspect}")
  end
end

puts "Validated production release command"
