#!/usr/bin/env ruby
# frozen_string_literal: true

require "open3"

REQUIRED_TOOLS = %w[
  docker
  minikube
  kubectl
  terraform
  go
  npm
  node
].freeze

OPTIONAL_TOOLS = %w[
  ngrok
  cloudflared
].freeze

def executable?(name)
  ENV.fetch("PATH", "").split(File::PATH_SEPARATOR).any? do |dir|
    path = File.join(dir, name)
    File.file?(path) && File.executable?(path)
  end
end

def command_output(*command)
  output, status = Open3.capture2e(*command)
  return "" unless status.success?

  output.lines.first.to_s.strip
end

def report_tool(name, required:)
  if executable?(name)
    version =
      case name
      when "docker" then command_output("docker", "--version")
      when "minikube" then command_output("minikube", "version", "--short")
      when "kubectl" then command_output("kubectl", "version", "--client=true", "--short")
      when "terraform" then command_output("terraform", "version")
      when "go" then command_output("go", "version")
      when "npm" then command_output("npm", "--version")
      when "node" then command_output("node", "--version")
      else command_output(name, "--version")
      end
    puts "ok #{name}: #{version.empty? ? 'installed' : version}"
    true
  else
    puts "#{required ? 'missing' : 'optional'} #{name}"
    !required
  end
end

ok = true
REQUIRED_TOOLS.each do |tool|
  ok = false unless report_tool(tool, required: true)
end
OPTIONAL_TOOLS.each do |tool|
  report_tool(tool, required: false)
end

if executable?("kubectl")
  context = command_output("kubectl", "config", "current-context")
  puts context.empty? ? "warn kubectl: no current context" : "info kubectl context: #{context}"
end

if executable?("minikube")
  status = command_output("minikube", "status", "--profile", "nova-sre", "--format", "{{.Host}}")
  puts status.empty? ? "warn minikube profile nova-sre is not running" : "info minikube nova-sre host: #{status}"
end

if OPTIONAL_TOOLS.none? { |tool| executable?(tool) }
  puts "warn tunnel: install ngrok or cloudflared before testing real GitHub webhooks"
end

abort("Local dependency check failed") unless ok
puts "Local dependency check passed"
