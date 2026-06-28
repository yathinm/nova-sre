#!/usr/bin/env ruby
# frozen_string_literal: true

require "English"

SKIPPED_PATHS = [
  %r{\Aagent-python/\.pytest_cache/},
  %r{\Afrontend/dist/}
].freeze

ALLOWLIST_PATTERNS = [
  /replace-with/i,
  /test-token/i,
  /top-secret/i,
  /webhook-secret/i,
  /control-panel-token/i,
  /agent-token/i,
  /super-secret-token/i,
  /\$[A-Z0-9_]+/,
  /<from [^>]+>/
].freeze

SECRET_PATTERNS = {
  "AWS access key" => /AKIA[0-9A-Z]{16}/,
  "GitHub token" => /gh[pousr]_[A-Za-z0-9_]{36,}/,
  "OpenAI API key" => /sk-(?:proj-)?[A-Za-z0-9_-]{20,}/,
  "Slack token" => /xox[baprs]-[A-Za-z0-9-]{20,}/,
  "private key block" => /-----BEGIN (?:RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----/
}.freeze

def tracked_files
  output = IO.popen(["git", "ls-files", "-z"], &:read)
  abort("git ls-files failed") unless $CHILD_STATUS&.success?

  output.split("\0").reject do |path|
    SKIPPED_PATHS.any? { |pattern| path.match?(pattern) }
  end
end

def text_file?(content)
  !content.include?("\x00")
end

findings = []

tracked_files.each do |path|
  content = File.binread(path)
  next unless text_file?(content)

  content.encode("UTF-8", invalid: :replace, undef: :replace).each_line.with_index(1) do |line, line_number|
    next if ALLOWLIST_PATTERNS.any? { |pattern| line.match?(pattern) }

    SECRET_PATTERNS.each do |label, pattern|
      findings << "#{path}:#{line_number}: possible #{label}" if line.match?(pattern)
    end
  end
end

if findings.any?
  warn findings.join("\n")
  exit 1
end

puts "No real-looking secrets found in tracked files"
