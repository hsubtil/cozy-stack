#!/usr/bin/env bash
set -ev

cd tests/integration
npm install -g @bitwarden/cli
bundle install --jobs=3 --retry=3
bundle exec ruby -e 'Dir.glob("tests/*.rb") { |f| load f }'
