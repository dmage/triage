#!/bin/sh -eu

if [ -z "${PRODUCTION-}" ]; then
    echo "DEVELOPMENT MODE" >&2
    PATH="$PWD/hack:$PATH"
fi

exec scraper serve --failure_data=./output/failure_data.json
