#!/bin/sh
# Generate fixtures and lay them out as the harness serves them (fx-small/, fx-large/).
set -e
python3 fakeserver.py gen fixtures
for s in small large; do
  mkdir -p fx-$s
  cp fixtures/deployments.json fixtures/statefulsets.json fixtures/secret.json fx-$s/
  cp fixtures/apps-$s.json fx-$s/apps.json
done
