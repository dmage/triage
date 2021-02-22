#!/bin/sh -eu

MAX_AGE=336h

if [ -z "${PRODUCTION-}" ]; then
    echo "DEVELOPMENT MODE" >&2
    PATH="$PWD/hack:$PATH"
    MAX_AGE=24h
    sleep() { exit; }
fi

mkdir -p ./output/slices

while true; do
    if [ ! -e ./cache/test-infra ]; then
        git clone https://github.com/kubernetes/test-infra.git ./cache/test-infra
    else
        (cd ./cache/test-infra && git pull --rebase) || {
            rm -rf ./cache/test-infra
            continue
        }
    fi

    scraper discover-testgrid ./cache/test-infra/config/testgrids/openshift/redhat-openshift-*.yaml --age="$MAX_AGE" -v=3
    scraper export-triage --builds=./tmp/triage_builds.json --tests=./tmp/triage_tests.json --age="$MAX_AGE" -v=2
    scraper cleanup --age="$MAX_AGE" -v=3
    triage \
        --builds=./tmp/triage_builds.json \
        --output=./output/failure_data_new.json \
        --output_slices=./output/slices/failure_data_PREFIX.json \
        --previous=./output/failure_data.json \
        ${NUM_WORKERS:+"--num_workers=${NUM_WORKERS}"} \
        ./tmp/triage_tests.json
    mv ./output/failure_data_new.json ./output/failure_data.json

    sleep 1800
done
