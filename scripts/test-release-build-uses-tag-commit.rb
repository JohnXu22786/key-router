#!/usr/bin/env ruby
# frozen_string_literal: true

require 'yaml'

workflow_path = File.expand_path('../.github/workflows/release.yml', __dir__)
workflow = YAML.safe_load_file(workflow_path, aliases: true)

def assert(condition, message)
  abort(message) unless condition
end

def active_run_lines(step)
  step.fetch('run').lines.map(&:strip).reject do |line|
    line.empty? || line.start_with?('#')
  end
end

version_job = workflow.fetch('jobs').fetch('version')
version_steps = version_job.fetch('steps')
outputs = version_job.fetch('outputs')
assert(
  outputs['commit_sha'] == '${{ steps.resolve_commit.outputs.commit_sha }}',
  'version job must publish the resolve_commit SHA'
)

tag_step = version_steps.find { |step| step.fetch('name', '').start_with?('Create and push tag') }
resolve_step = version_steps.find { |step| step['id'] == 'resolve_commit' }
assert(tag_step, 'version job must create or reuse the requested tag')
assert(resolve_step, 'version job must have a resolve_commit step')
assert(version_steps.index(resolve_step) > version_steps.index(tag_step), 'commit must resolve after tag creation/fetch')
assert(
  resolve_step.dig('env', 'RELEASE_TAG') == '${{ steps.resolve.outputs.tag }}',
  'resolve_commit must receive the tag selected by the version resolver'
)

resolve_lines = active_run_lines(resolve_step)
peel_command = 'commit_sha=$(git rev-parse --verify "${RELEASE_TAG}^{commit}")'
output_command = 'echo "commit_sha=$commit_sha" >> "$GITHUB_OUTPUT"'
peel_index = resolve_lines.index(peel_command)
output_index = resolve_lines.index(output_command)
assert(peel_index && output_index && peel_index < output_index, 'resolve_commit must publish the peeled tag commit SHA')

tag_lines = active_run_lines(tag_step)
push_index = tag_lines.index('if ! git push origin "$RELEASE_TAG"; then')
race_check_index = tag_lines.index('if ! git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then')
fetch_index = tag_lines.index('git fetch --no-tags --force origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"')
assert(push_index && race_check_index && fetch_index && push_index < race_check_index && race_check_index < fetch_index,
       'failed tag pushes must confirm and fetch the winning remote tag before commit resolution')

build_job = workflow.fetch('jobs').fetch('build')
needs = Array(build_job.fetch('needs'))
assert(needs.include?('version'), 'build job must depend on the version job')

checkout_steps = build_job.fetch('steps').select do |step|
  uses = step['uses']
  uses.is_a?(String) && uses.start_with?('actions/checkout@')
end
assert(!checkout_steps.empty?, 'build job must contain a checkout step')

checkout_steps.each do |step|
  with = step['with']
  ref = with['ref'] if with.is_a?(Hash)
  assert(ref == '${{ needs.version.outputs.commit_sha }}', 'every build checkout must use the resolved commit SHA')
end

puts 'release build tag commit checks passed'
