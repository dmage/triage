#!/bin/sh -eu
docker build -t quay.io/rh-obulatov/triage .
docker push quay.io/rh-obulatov/triage
IMAGE=$(docker inspect quay.io/rh-obulatov/triage | jq -r '.[].RepoDigests[]' | head -n1)
cd ./manifests
kustomize edit set image "$IMAGE"
