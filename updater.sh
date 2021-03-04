#!/bin/sh -eu

MAX_AGE=336h

if [ -z "${PRODUCTION-}" ]; then
    echo "DEVELOPMENT MODE" >&2
    PATH="$PWD/hack:$PATH"
    MAX_AGE=24h
    sleep() { exit; }
fi

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
    mkdir -p ./output/new/slices
    triage \
        --builds=./tmp/triage_builds.json \
        --output=./output/new/failure_data.json \
        --output_slices=./output/new/slices/failure_data_PREFIX.json \
        --previous=./output/failure_data.json \
        ${NUM_WORKERS:+"--num_workers=${NUM_WORKERS}"} \
        ./tmp/triage_tests.json
    (cd ./output/new && tar -cf ./failure_data.tar -- *)
    mv ./output/new/slices/* ./output/slices
    mv ./output/new/failure_data.json ./output/new/failure_data.tar ./output
    rm -rf ./output/new

    sleep 1800
done
