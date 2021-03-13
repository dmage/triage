#!/bin/sh -eu

if [ -z "${PRODUCTION-}" ]; then
    echo "DEVELOPMENT MODE" >&2
    PATH="$PWD/hack:$PATH"
fi

while true; do
    if [ -e ./output/failure_data.json ]; then
        break
    fi
    printf "Waiting for ./output/failure_data.json...\n" >&2
    sleep 5
done

exec scraper serve --failure_data=./output/
