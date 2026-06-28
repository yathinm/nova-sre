#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

paths = Dir["k8s/{base,rbac}/**/*.yaml"].sort
abort("no Kubernetes YAML files found") if paths.empty?

errors = []
documents = 0

paths.each do |path|
  YAML.load_stream(File.read(path)).each_with_index do |document, index|
    next if document.nil?

    documents += 1
    unless document.is_a?(Hash)
      errors << "#{path}:#{index + 1}: document must be a mapping"
      next
    end

    api_version = document["apiVersion"].to_s.strip
    kind = document["kind"].to_s.strip
    metadata = document["metadata"]
    name = metadata.is_a?(Hash) ? metadata["name"].to_s.strip : ""

    errors << "#{path}:#{index + 1}: missing apiVersion" if api_version.empty?
    errors << "#{path}:#{index + 1}: missing kind" if kind.empty?
    errors << "#{path}:#{index + 1}: missing metadata.name" if name.empty?
  rescue Psych::SyntaxError => error
    errors << "#{path}: YAML syntax error: #{error.message}"
  end
end

errors << "no Kubernetes YAML documents found" if documents.zero?

if errors.any?
  warn errors.join("\n")
  exit 1
end

puts "Validated #{documents} Kubernetes YAML documents"
