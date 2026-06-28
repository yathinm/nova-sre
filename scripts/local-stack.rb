#!/usr/bin/env ruby
# frozen_string_literal: true

require "fileutils"
require "open3"
require "socket"

STATE_DIR = ".local"
LOG_DIR = File.join(STATE_DIR, "logs")
FORWARDS = {
  "server" => {
    pid: File.join(STATE_DIR, "port-forward-server.pid"),
    log: File.join(LOG_DIR, "port-forward-server.log"),
    command: %w[kubectl port-forward -n nova-sre svc/nova-sre-server 8080:8080],
    port: "8080"
  },
  "frontend" => {
    pid: File.join(STATE_DIR, "port-forward-frontend.pid"),
    log: File.join(LOG_DIR, "port-forward-frontend.log"),
    command: %w[kubectl port-forward -n nova-sre svc/nova-sre-frontend 8081:80],
    port: "8081"
  }
}.freeze

def env_truthy?(name)
  %w[1 true yes].include?(ENV.fetch(name, "").strip.downcase)
end

def run!(*command)
  puts "+ #{command.join(' ')}"
  system(*command) || abort("#{command.join(' ')} failed")
end

def pid_running?(pid)
  Process.kill(0, pid)
  true
rescue Errno::ESRCH
  false
rescue Errno::EPERM
  true
end

def port_open?(port)
  socket = TCPSocket.new("127.0.0.1", port.to_i)
  socket.close
  true
rescue Errno::ECONNREFUSED, Errno::EHOSTUNREACH
  false
end

def stop_forward(name, config)
  return unless File.exist?(config[:pid])

  pid = File.read(config[:pid]).strip.to_i
  FileUtils.rm_f(config[:pid])
  return if pid <= 0 || !pid_running?(pid)

  puts "Stopping #{name} port-forward pid=#{pid}"
  Process.kill("TERM", pid)
rescue Errno::ESRCH
  nil
end

def start_forward(name, config)
  stop_forward(name, config)
  if port_open?(config[:port])
    puts "Using existing #{name} listener on localhost:#{config[:port]}"
    return
  end

  FileUtils.mkdir_p(LOG_DIR)
  log = File.open(config[:log], "a")
  pid = Process.spawn(*config[:command], out: log, err: log)
  log.close
  File.write(config[:pid], "#{pid}\n")
  puts "Started #{name} port-forward on localhost:#{config[:port]} pid=#{pid} log=#{config[:log]}"
end

def wait_for_runtime
  20.times do
    return true if system("ruby", "scripts/validate-local-runtime.rb", out: File::NULL, err: File::NULL)

    sleep 1
  end
  false
end

action = ARGV.fetch(0, "up")
case action
when "up"
  FileUtils.mkdir_p(STATE_DIR)
  run!("kubectl", "apply", "-f", "k8s/base/namespace.yaml")
  run!("ruby", "scripts/sync-k8s-secret.rb") if env_truthy?("NOVA_SRE_LOCAL_SYNC_SECRET")
  run!("make", "docker-build") unless env_truthy?("NOVA_SRE_LOCAL_SKIP_BUILD")
  run!("make", "deploy-apps") unless env_truthy?("NOVA_SRE_LOCAL_SKIP_DEPLOY")
  FORWARDS.each { |name, config| start_forward(name, config) }
  abort("local runtime did not become ready; inspect .local/logs/*.log") unless wait_for_runtime

  run!("make", "validate-local-runtime")
  puts "Nova-SRE local stack is ready: http://localhost:8081"
when "down"
  FORWARDS.each { |name, config| stop_forward(name, config) }
  puts "Stopped managed Nova-SRE port-forwards"
when "status"
  FORWARDS.each do |name, config|
    pid = File.exist?(config[:pid]) ? File.read(config[:pid]).strip.to_i : 0
    state =
      if pid.positive? && pid_running?(pid)
        "managed pid=#{pid}"
      elsif port_open?(config[:port])
        "external listener on localhost:#{config[:port]}"
      else
        "stopped"
      end
    puts "#{name}: #{state}"
  end
else
  abort("usage: ruby scripts/local-stack.rb [up|down|status]")
end
