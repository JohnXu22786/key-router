#!/usr/bin/env ruby
# frozen_string_literal: true

require 'yaml'
require 'open3'
require 'tmpdir'

workflow_path = File.expand_path('../.github/workflows/release.yml', __dir__)
workflow = YAML.safe_load_file(workflow_path, aliases: true)
ensure_script = File.expand_path('ensure-release-tag.sh', __dir__)
resolve_script = File.expand_path('resolve-release-tag-commit.sh', __dir__)

def assert(condition, message)
  abort(message) unless condition
end

def command!(directory, *command, env: {})
  stdout, stderr, status = Open3.capture3(env, *command, chdir: directory)
  return stdout.strip if status.success?

  abort("command failed: #{command.join(' ')}\n#{stdout}\n#{stderr}")
end

def git!(repository, *arguments)
  command!(repository, 'git', *arguments)
end

def seed_remote(root, name)
  origin = File.join(root, "#{name}.git")
  seed = File.join(root, "#{name}-seed")
  git!(root, 'init', '--bare', '--initial-branch=main', origin)
  git!(root, 'init', '--initial-branch=main', seed)
  git!(seed, 'config', 'user.name', 'Release regression')
  git!(seed, 'config', 'user.email', 'release-regression@example.invalid')
  git!(seed, 'commit', '--allow-empty', '-m', "#{name} winner")
  winner = git!(seed, 'rev-parse', 'HEAD')
  git!(seed, 'remote', 'add', 'origin', origin)
  git!(seed, 'push', 'origin', 'main')
  [origin, seed, winner]
end

def clone_runner(root, origin, name)
  runner = File.join(root, name)
  git!(root, 'clone', origin, runner)
  git!(runner, 'config', 'user.name', 'Release regression')
  git!(runner, 'config', 'user.email', 'release-regression@example.invalid')
  runner
end

def ensure_tag(runner, script, tag, extra_env = {})
  command!(runner, 'bash', script, env: extra_env.merge('RELEASE_TAG' => tag))
end

def assert_resolved_commit(runner, script, tag, expected, output_path)
  File.write(output_path, '')
  command!(runner, 'bash', script, env: {
    'RELEASE_TAG' => tag,
    'GITHUB_OUTPUT' => output_path
  })
  lines = File.readlines(output_path, chomp: true)
  assert(lines == ["commit_sha=#{expected}"], "#{tag} must publish its peeled commit SHA")
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
  tag_step.dig('env', 'RELEASE_TAG') == '${{ steps.resolve.outputs.tag }}',
  'tag creation must receive the tag selected by the version resolver'
)
assert(
  resolve_step.dig('env', 'RELEASE_TAG') == '${{ steps.resolve.outputs.tag }}',
  'resolve_commit must receive the tag selected by the version resolver'
)
assert(
  tag_step.fetch('run').strip == 'bash "$GITHUB_WORKSPACE/scripts/ensure-release-tag.sh"',
  'tag workflow step must directly run ensure-release-tag.sh'
)
assert(
  resolve_step.fetch('run').strip == 'bash "$GITHUB_WORKSPACE/scripts/resolve-release-tag-commit.sh"',
  'resolve_commit workflow step must directly run resolve-release-tag-commit.sh'
)

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

Dir.mktmpdir('release-tag-regression-') do |root|
  new_origin, _new_seed, new_head = seed_remote(root, 'new-tag')
  new_runner = clone_runner(root, new_origin, 'new-tag-runner')
  new_tag = 'v9.8.1'
  ensure_tag(new_runner, ensure_script, new_tag)
  assert(git!(new_runner, 'rev-parse', "#{new_tag}^{commit}") == new_head, 'new tags must point at the checked-out commit')
  remote_new_tag = command!(root, 'git', "--git-dir=#{new_origin}", 'rev-parse', "refs/tags/#{new_tag}^{commit}")
  assert(remote_new_tag == new_head, 'new release tags must be pushed to origin')

  existing_origin, existing_seed, existing_commit = seed_remote(root, 'existing-tag')
  existing_tag = 'v9.8.2'
  git!(existing_seed, 'tag', '-a', existing_tag, '-m', 'annotated release', existing_commit)
  git!(existing_seed, 'push', 'origin', existing_tag)
  existing_runner = clone_runner(root, existing_origin, 'existing-tag-runner')
  git!(existing_runner, 'commit', '--allow-empty', '-m', 'dispatch branch commit')
  dispatch_commit = git!(existing_runner, 'rev-parse', 'HEAD')
  assert(dispatch_commit != existing_commit, 'existing-tag fixture must differ from dispatch HEAD')
  git!(existing_runner, 'tag', '--force', existing_tag, dispatch_commit)
  ensure_tag(existing_runner, ensure_script, existing_tag)
  assert(git!(existing_runner, 'cat-file', '-t', "refs/tags/#{existing_tag}") == 'tag', 'existing annotated tags must be preserved')
  assert(git!(existing_runner, 'rev-parse', "#{existing_tag}^{commit}") == existing_commit, 'existing tags must resolve to their remote commit')
  assert_resolved_commit(existing_runner, resolve_script, existing_tag, existing_commit, File.join(root, 'existing-output'))

  race_origin, _race_seed, race_winner = seed_remote(root, 'race-tag')
  race_runner = clone_runner(root, race_origin, 'race-tag-runner')
  git!(race_runner, 'commit', '--allow-empty', '-m', 'dispatch branch commit')
  race_loser = git!(race_runner, 'rev-parse', 'HEAD')
  race_tag = 'v9.8.3'
  hook = File.join(race_runner, '.git', 'hooks', 'pre-push')
  File.write(hook, <<~BASH)
    #!/usr/bin/env bash
    set -euo pipefail
    git --git-dir="$RACE_ORIGIN" update-ref "refs/tags/$RELEASE_TAG" "$RACE_WINNER"
    exit 1
  BASH
  File.chmod(0o755, hook)
  assert(race_loser != race_winner, 'race fixture commits must differ')
  ensure_tag(race_runner, ensure_script, race_tag, {
    'RACE_ORIGIN' => race_origin,
    'RACE_WINNER' => race_winner
  })
  assert(git!(race_runner, 'rev-parse', "#{race_tag}^{commit}") == race_winner, 'a failed push must fetch the winning remote tag')
  assert_resolved_commit(race_runner, resolve_script, race_tag, race_winner, File.join(root, 'race-output'))
end

puts 'release build tag commit checks passed'
